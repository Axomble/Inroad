// Package sender is the execution-plane email send handler.
package sender

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/queue"
	"github.com/inroad/inroad/internal/worker/personalize"
	"github.com/inroad/inroad/internal/worker/track"
)

// Sender sends one email through whichever transport the job's Provider selects
// (SMTP or Gmail). Defined here (consumer side) so the handler depends on the
// behavior, not the concrete *mail.MultiSender — which lets tests inject a fake
// and exercise the full pipeline without a live server.
type Sender interface {
	Send(ctx context.Context, tj mail.OutboundJob, msg mail.Message) (messageID string, err error)
}

// maxSendAttempts caps the cap-exceeded re-enqueue loop so a send that
// keeps hitting a daily ceiling it can never clear (misconfigured cap,
// stuck sent-today counter) doesn't cycle forever.
const maxSendAttempts = 30

// campaignPausedBackoff is how long a send waits while its campaign is not
// running. A relaunch happens on no schedule the worker can predict, so it polls
// on the same 6h interval the cap-exceeded path uses.
const campaignPausedBackoff = 6 * time.Hour

// Handler returns an asynq handler for send:email tasks. publicURL and
// trackingSecret are the base URL and HMAC secret used to build/sign open and
// click tracking links (internal/worker/track) when the job's campaign has
// tracking enabled.
func Handler(core coreapi.Client, sender Sender, enq DelayedSendEnqueuer, publicURL string, trackingSecret []byte) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, t *asynq.Task) error {
		var p queue.SendEmailPayload
		if err := json.Unmarshal(t.Payload(), &p); err != nil {
			return err
		}
		job, err := core.GetSendJob(ctx, p.SendID, p.WorkspaceID)
		if err != nil {
			return err
		}
		// Zeroize the decrypted SMTP password before returning so the []byte
		// carrying it doesn't linger past this handler. The gomail client
		// receives a string copy (immutable, transient) which we can't
		// reach; but the primary long-lived buffer is the one we allocated,
		// so wiping this closes the window an in-process memory dump would
		// have to catch.
		defer zeroize(job.SMTPPassword)
		// The gmail access token is a decrypted secret too; wipe it after the send
		// for the same reason as the SMTP password.
		defer zeroize(job.AccessToken)

		if job.Suppressed {
			return core.MarkSend(ctx, p.SendID, p.WorkspaceID, coreapi.SendResult{Status: "skipped"})
		}
		// The campaign is not running: paused (by hand or by the deliverability
		// circuit breaker), draft, or done. Deferred rather than finalized, matching
		// the step path — a pause is a condition that CLEARS, and the row must
		// survive to be sent when an operator relaunches. Attempts is deliberately
		// NOT bumped: maxSendAttempts exists to kill a ceiling that can never clear,
		// and charging a paused campaign against it would fail the send after ~7.5
		// days of a pause the operator chose.
		//
		// This path is DORMANT (nothing in production creates 'queued' campaign
		// sends); the gate is here so the invariant is a property of the codebase
		// rather than of the one live path.
		if job.CampaignPaused {
			return enq.EnqueueSendIn(p.SendID, p.WorkspaceID, campaignPausedBackoff)
		}
		if job.SentToday >= job.EffectiveDailyCap {
			// Over today's cap. Bump attempts and re-enqueue for the next
			// daily window — but fail out if we've been looping too long, so
			// a permanently mis-set cap can't monopolize the queue.
			n, err := core.IncrementSendAttempts(ctx, p.SendID, p.WorkspaceID)
			if err != nil {
				return err
			}
			if n > maxSendAttempts {
				return core.MarkSend(ctx, p.SendID, p.WorkspaceID, coreapi.SendResult{
					Status: "failed", Err: "cap exceeded (max attempts)",
				})
			}
			return enq.EnqueueSendIn(p.SendID, p.WorkspaceID, 6*time.Hour)
		}

		// Claim-before-send: move the pre-existing 'queued' row to 'sending' (or
		// reclaim a stale lease). If we don't win the claim, another worker owns
		// it or it is already terminal — skip the send so a retried/raced job can
		// never double-deliver. Unlike the step path, this path has no enrollment
		// cursor, so "recover-forward" reduces to a plain skip: a 'sent' row is
		// never reclaimed (the ClaimSend WHERE matches only 'queued' OR a stale
		// 'sending', so 'sent' is excluded), so a lost claim means the send is
		// already done — nothing left to advance. The one residual double-send
		// window (a hard crash between Send returning success and the single
		// MarkSend commit) is inherent to non-transactional SMTP, same as the step
		// path's MarkStepDelivered window; this path is dormant (EnqueueSends is
		// test-only) so no deeper rework is warranted.
		claimed, err := core.ClaimSend(ctx, p.SendID, p.WorkspaceID)
		if err != nil {
			return err
		}
		if !claimed {
			return nil
		}

		// Subject is a header, treated as text: no HTML escape.
		vars := personalize.Vars{FirstName: job.FirstName, Email: job.ToEmail}
		subject := personalize.Text(job.Subject, vars)
		bodyText := withUnsubText(personalize.Text(job.BodyText, vars), job.UnsubURL)
		bodyHTML := ""
		if job.BodyHTML != "" {
			bodyHTML = withUnsubHTML(personalize.HTML(job.BodyHTML, vars), job.UnsubURL)
			// Tracking rewrite runs AFTER the unsub footer so the unsubscribe
			// link is present in the body when RewriteHTML skips it (never
			// click-tracked).
			if job.TrackingEnabled {
				bodyHTML = track.RewriteHTML(bodyHTML, publicURL, job.SendID, trackingSecret)
			}
		}

		msgID, sendErr := sender.Send(ctx,
			mail.OutboundJob{
				Provider: job.Provider, Host: job.SMTPHost, Port: job.SMTPPort,
				Username: job.SMTPUsername, Password: string(job.SMTPPassword),
				AllowPlaintext: job.AllowPlaintext, AccessToken: string(job.AccessToken),
			},
			mail.Message{
				FromEmail: job.FromEmail, FromName: job.FromName, To: job.ToEmail,
				Subject: subject, BodyText: bodyText, BodyHTML: bodyHTML, ListUnsubscribe: job.UnsubURL,
			},
		)
		switch {
		case sendErr == nil:
			return core.MarkSend(ctx, p.SendID, p.WorkspaceID, coreapi.SendResult{Status: "sent", MessageID: msgID})
		case mail.Retryable(sendErr):
			// Transient failure (nothing delivered): release the claim and return
			// the error so asynq retries.
			if rerr := core.ReleaseSend(ctx, p.SendID, p.WorkspaceID); rerr != nil {
				return fmt.Errorf("release send after transient failure: %w", rerr)
			}
			return sendErr
		default:
			// Permanent failure: finalize 'failed' (fail-forward).
			return core.MarkSend(ctx, p.SendID, p.WorkspaceID, coreapi.SendResult{Status: "failed", Err: sendErr.Error()})
		}
	}
}

// zeroize overwrites b in place. Go strings are immutable so this only
// works on the []byte form — hence SendJob.SMTPPassword is bytes, not string.
func zeroize(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

func withUnsubText(body, url string) string {
	if url == "" {
		return body
	}
	return body + "\n\n---\nUnsubscribe: " + url
}

func withUnsubHTML(body, url string) string {
	if url == "" {
		return body
	}
	return body + `<hr><p style="font-size:12px;color:#888">` +
		`<a href="` + url + `">Unsubscribe</a></p>`
}
