// Package sender is the execution-plane email send handler.
package sender

import (
	"context"
	"encoding/json"
	"time"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/queue"
	"github.com/inroad/inroad/internal/worker/personalize"
	"github.com/inroad/inroad/internal/worker/track"
)

// Sender sends one email over SMTP. Defined here (consumer side) so the handler
// depends on the behavior, not the concrete *mail.NetSender — which lets tests
// inject a fake and exercise the full pipeline without a live SMTP server.
type Sender interface {
	Send(cfg mail.SMTPConfig, msg mail.Message) (messageID string, err error)
}

// maxSendAttempts caps the cap-exceeded re-enqueue loop so a send that
// keeps hitting a daily ceiling it can never clear (misconfigured cap,
// stuck sent-today counter) doesn't cycle forever.
const maxSendAttempts = 30

// Handler returns an asynq handler for send:email tasks. publicURL and
// trackingSecret are the base URL and HMAC secret used to build/sign open and
// click tracking links (internal/worker/track) when the job's campaign has
// tracking enabled.
func Handler(core coreapi.Client, sender Sender, enq *queue.Client, publicURL string, trackingSecret []byte) func(context.Context, *asynq.Task) error {
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

		if job.Suppressed {
			return core.MarkSend(ctx, p.SendID, p.WorkspaceID, coreapi.SendResult{Status: "skipped"})
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

		msgID, sendErr := sender.Send(
			mail.SMTPConfig{Host: job.SMTPHost, Port: job.SMTPPort, Username: job.SMTPUsername, Password: string(job.SMTPPassword), UseTLS: job.UseTLS},
			mail.Message{
				FromEmail: job.FromEmail, FromName: job.FromName, To: job.ToEmail,
				Subject: subject, BodyText: bodyText, BodyHTML: bodyHTML, ListUnsubscribe: job.UnsubURL,
			},
		)
		if sendErr != nil {
			return core.MarkSend(ctx, p.SendID, p.WorkspaceID, coreapi.SendResult{Status: "failed", Err: sendErr.Error()})
		}
		return core.MarkSend(ctx, p.SendID, p.WorkspaceID, coreapi.SendResult{Status: "sent", MessageID: msgID})
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
