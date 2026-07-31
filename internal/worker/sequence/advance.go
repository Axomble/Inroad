// Package sequence is the execution-plane multi-step sequencing engine: the
// sequence:advance handler sends a contact's next due step and schedules the
// following one (lazy chain), and the enrollment sweeper reconciles anything
// left behind.
package sequence

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

// Sender sends one email through the transport the job's Provider selects (same
// contract as the direct sender). Defined here so tests inject a fake and
// exercise the pipeline without a live server.
type Sender interface {
	Send(ctx context.Context, tj mail.OutboundJob, msg mail.Message) (messageID string, err error)
}

// Enqueuer schedules the next advance. Satisfied by *queue.Client.
type Enqueuer interface {
	EnqueueAdvanceAt(enrollmentID, workspaceID string, t time.Time) error
	EnqueueAdvanceIn(enrollmentID, workspaceID string, d time.Duration) error
}

// capBackoff is how long to wait before retrying an enrollment blocked by the
// mailbox's daily cap. Matches the direct sender's 6h re-enqueue.
const capBackoff = 6 * time.Hour

// minBlockedBackoff floors the campaign-limit wait so an advance that lands in the
// last seconds of a UTC day doesn't re-enqueue itself immediately and spin.
const minBlockedBackoff = time.Minute

// blockedBackoff is how long to wait before re-attempting a step blocked by
// something other than this mailbox's own cap. It picks the moment the block can
// actually clear, rather than a fixed retry:
//
//   - a campaign at its daily_limit clears at the next UTC midnight, when the
//     allowance resets — retrying sooner just burns a task to learn nothing;
//   - a warmup-paused mailbox clears whenever the health sweep steps it back down,
//     which is not on a schedule this worker can predict, so it reuses capBackoff.
//
// A campaign that is BOTH limited and paused takes the shorter wait: the sooner
// signal is the one worth re-checking.
func blockedBackoff(job coreapi.StepSendJob, now time.Time) time.Duration {
	if !job.CampaignLimited {
		return capBackoff
	}
	utc := now.UTC()
	untilMidnight := time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC).
		AddDate(0, 0, 1).Sub(utc)
	if untilMidnight < minBlockedBackoff {
		untilMidnight = minBlockedBackoff
	}
	if job.HealthPaused && capBackoff < untilMidnight {
		return capBackoff
	}
	return untilMidnight
}

// maxCapDeferrals bounds the cap-exceeded re-enqueue loop, mirroring
// sender.maxSendAttempts: an enrollment that keeps hitting a daily cap it can
// never clear (mis-set cap, stuck sent-today counter) is failed instead of
// deferring forever.
const maxCapDeferrals = 30

// These mirror enrollment.StopSuppressed / StopFailed / StopMailboxRemoved.
// Duplicated as string constants here because the worker reaches the enrollment
// domain only through coreapi (app/* isolation); the values must stay identical.
const (
	stopReasonSuppressed     = "suppressed"
	stopReasonFailed         = "failed"
	stopReasonMailboxRemoved = "mailbox_removed"
)

// AdvanceHandler returns an asynq handler for sequence:advance tasks. It owns
// the whole step lifecycle: fetch the due step, personalize, build a threaded
// MIME message, send over SMTP, record the result + advance the cursor, and
// (lazy chain) schedule the next step — or stop. publicURL and trackingSecret
// are the base URL and HMAC secret used to build/sign open and click tracking
// links (internal/worker/track) when the step's campaign has tracking enabled.
func AdvanceHandler(core coreapi.Client, sender Sender, enq Enqueuer, publicURL string, trackingSecret []byte) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, t *asynq.Task) error {
		var p queue.AdvancePayload
		if err := json.Unmarshal(t.Payload(), &p); err != nil {
			return err
		}
		job, err := core.GetStepSendJob(ctx, p.EnrollmentID, p.WorkspaceID)
		if err != nil {
			return err
		}
		defer zeroize(job.SMTPPassword)
		// The gmail access token is a decrypted secret too; wipe it after use.
		defer zeroize(job.AccessToken)

		// Enrollment no longer active (stopped/completed) or no next step.
		if job.Skip {
			return nil
		}
		if job.Suppressed {
			return core.MarkStepStopped(ctx, p.EnrollmentID, p.WorkspaceID, stopReasonSuppressed)
		}
		if job.MailboxRemoved {
			// The thread's sending mailbox was deleted mid-sequence, so the thread
			// has no identity to continue: any follow-up would come from a stranger
			// referencing a Message-ID it never sent. Stop through the same single
			// entry point as every other halt; nothing is sent.
			return core.MarkStepStopped(ctx, p.EnrollmentID, p.WorkspaceID, stopReasonMailboxRemoved)
		}
		// deferForCapacity bumps the deferral counter and retries later, leaving the
		// cursor unchanged so the same step is re-attempted — but fails out if we've
		// deferred too long, so a ceiling that can never clear (a mis-set cap, a
		// mailbox that never recovers) can't re-enqueue this enrollment forever.
		deferForCapacity := func() error {
			n, err := core.IncrementEnrollmentCapDeferrals(ctx, p.EnrollmentID, p.WorkspaceID)
			if err != nil {
				return err
			}
			if n > maxCapDeferrals {
				return core.MarkStepStopped(ctx, p.EnrollmentID, p.WorkspaceID, stopReasonFailed)
			}
			return enq.EnqueueAdvanceIn(p.EnrollmentID, p.WorkspaceID, capBackoff)
		}
		// Blocked by something that is NOT this mailbox's own cap: the campaign has
		// hit its campaign-wide daily limit, or the warmup engine has paused the
		// mailbox this thread must send from.
		//
		// Checked BEFORE the degenerate-cap branch below, which STOPS an enrollment:
		// an unhealthy mailbox may recover and a limited campaign gets a fresh
		// allowance tomorrow, so the thread waits rather than dies — and it cannot
		// be re-routed, since a follow-up must come from the mailbox that started
		// the thread.
		//
		// Deliberately NOT deferForCapacity: that budget exists to kill a ceiling
		// that can never clear (a mis-set cap, a stuck counter), and neither of
		// these is one. A campaign daily_limit is a setting the operator chose —
		// daily_limit 10 over 1000 contacts is a correctly configured 100-day
		// campaign, and charging its waiting enrollments against a 30 × 6h ≈ 7.5-day
		// budget would mark ~99% of them 'failed' for working as instructed. A
		// warmup pause is timed and self-clearing. So these wait indefinitely,
		// staying 'active' and visible in the UI, which is the honest state.
		if job.CampaignLimited || job.HealthPaused {
			return enq.EnqueueAdvanceIn(p.EnrollmentID, p.WorkspaceID, blockedBackoff(job, time.Now()))
		}
		if job.EffectiveDailyCap <= 0 {
			// Degenerate cap (daily_cap=0, or ramp_start_cap=0 on a brand-new
			// mailbox): this enrollment can never send. Stop it 'failed' rather
			// than deferring forever.
			return core.MarkStepStopped(ctx, p.EnrollmentID, p.WorkspaceID, stopReasonFailed)
		}
		if job.SentToday >= job.EffectiveDailyCap {
			// Over today's cap — including a cap the mailbox's warmup health has
			// scaled down, which is enforced here without a branch of its own.
			return deferForCapacity()
		}

		// schedule enqueues the next step (lazy chain) unless the enrollment
		// completed. advanceCursor advances the enrollment cursor in its own
		// committed step, then schedules — shared by the normal success path and
		// recover-forward.
		schedule := func(adv coreapi.Advance) error {
			if !adv.Completed {
				return enq.EnqueueAdvanceAt(p.EnrollmentID, p.WorkspaceID, adv.NextDueAt)
			}
			return nil
		}
		advanceCursor := func() error {
			adv, err := core.AdvanceStepCursor(ctx, job)
			if err != nil {
				return err
			}
			return schedule(adv)
		}

		// Claim-before-send: the sends row is the delivery claim.
		//   - ClaimSkip: another worker owns a fresh 'sending' lease, or the row is
		//     terminal — nothing to do.
		//   - ClaimAlreadySent: a prior run delivered THIS exact step but its cursor
		//     advance didn't commit. Recover-forward: advance the cursor only, never
		//     re-send. This is what closes the residual double-send window.
		//   - ClaimWon: we own the claim; build + send below.
		outcome, err := core.ClaimStepSend(ctx, job)
		if err != nil {
			return err
		}
		switch outcome {
		case coreapi.ClaimSkip:
			return nil
		case coreapi.ClaimDeferred:
			delay := time.Duration(job.MinIntervalSeconds) * time.Second
			if delay < time.Second {
				delay = time.Second
			}
			return enq.EnqueueAdvanceIn(p.EnrollmentID, p.WorkspaceID, delay)
		case coreapi.ClaimAlreadySent:
			return advanceCursor()
		}

		vars := personalize.Vars{
			FirstName: job.Vars.FirstName, LastName: job.Vars.LastName,
			Email: job.Vars.Email, Company: job.Vars.Company, Custom: job.Vars.Custom,
		}
		subject := personalize.Text(job.Subject, vars)
		bodyText := withUnsubText(personalize.Text(job.BodyText, vars), job.UnsubURL)
		bodyHTML := ""
		if job.BodyHTML != "" {
			bodyHTML = withUnsubHTML(personalize.HTML(job.BodyHTML, vars), job.UnsubURL)
			// Tracking rewrite runs AFTER the unsub footer so the unsubscribe
			// link is present in the body when RewriteHTML skips it (never
			// click-tracked). Uses the pre-derived SendID (== the claimed row id).
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
				Subject: subject, BodyText: bodyText, BodyHTML: bodyHTML,
				ListUnsubscribe: job.UnsubURL, InReplyTo: job.InReplyTo, References: job.References,
			},
		)

		switch {
		case sendErr == nil:
			// Durable-delivered + recover-forward: commit status='sent' FIRST so the
			// delivery is durable on its own, THEN advance the cursor as a separate
			// committed step. If the cursor advance fails, the asynq retry's claim
			// sees the 'sent' row (ClaimAlreadySent) and advances without re-sending.
			if err := core.MarkStepDelivered(ctx, job, msgID); err != nil {
				// The status='sent' UPDATE failed: the row is still 'sending', so
				// returning the error lets asynq retry. This is the irreducible
				// one-UPDATE + hard-crash window documented on MarkStepDelivered.
				return fmt.Errorf("mark step delivered: %w", err)
			}
			return advanceCursor()
		case mail.Retryable(sendErr):
			// Transient failure (nothing delivered): release the claim so the
			// asynq retry reclaims it, and return the error so asynq retries.
			if rerr := core.ReleaseStepSend(ctx, job); rerr != nil {
				return fmt.Errorf("release step send after transient failure: %w", rerr)
			}
			return sendErr
		default:
			// Permanent failure: fail-forward — finalize 'failed' and advance the
			// cursor in one tx so a single bad step doesn't wedge the enrollment.
			adv, ferr := core.FinalizeStepSend(ctx, job, coreapi.StepResult{Status: "failed", Err: sendErr.Error()})
			if ferr != nil {
				return ferr
			}
			return schedule(adv)
		}
	}
}

// zeroize overwrites the decrypted SMTP password in place after use.
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
