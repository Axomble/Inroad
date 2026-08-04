// Package warmup is the execution-plane warmup engine. The warmup:tick handler
// sends one warmup email (a new-thread opener or a threaded reply) and schedules
// the mailbox's next tick (lazy chain), mirroring sequence/advance.go's
// claim→send→finalize→schedule shape; the warmup:sweep handler fans out a tick
// per due participant and recomputes health. Both reach data ONLY through
// coreapi and send ONLY through the shared SSRF-guarded transport — no new DB or
// dial path.
package warmup

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/metrics"
	"github.com/inroad/inroad/internal/platform/queue"
)

// sendKind labels every metric this handler emits — kind="warmup", the other
// half of inroad_sends_total's kind label (sequence.AdvanceHandler emits
// "campaign").
const sendKind = "warmup"

// warmupHeader is the custom MIME header carrying the signed receipt token
// (spec §7). The recipient's inbox poller verifies it to recognize warmup mail;
// it MUST be emitted on the wire, so it is set through mail.Message.ExtraHeaders,
// which every transport serializes.
const warmupHeader = "X-Inroad-Warmup"

// Sender sends one email through the transport the job's Provider selects (same
// contract as the campaign send path). Defined here so tests inject a fake and
// exercise the pipeline without a live server.
type Sender interface {
	Send(ctx context.Context, tj mail.OutboundJob, msg mail.Message) (messageID string, err error)
}

// Enqueuer schedules a warmup:tick at a time, routed to a worker queue.
// Satisfied by *queue.Client.
type Enqueuer interface {
	EnqueueWarmupTickAt(mailboxID, workspaceID string, t time.Time, dest string) error
}

// SendHandler returns an asynq handler for warmup:tick tasks. It owns one warmup
// send's lifecycle: fetch the next action, claim it (claim-before-send
// idempotency), build the threaded MIME message with the signed X-Inroad-Warmup
// receipt header, send over the shared SSRF-guarded transport, finalize, and
// (lazy chain) schedule the mailbox's next tick — exactly one live tick per
// mailbox. Mirrors sequence.AdvanceHandler. mtx records inroad_sends_total at
// each finalize point (see the result= comment at each call site below); a
// nil mtx (metrics disabled, or a test that doesn't need one) no-ops.
func SendHandler(core coreapi.Client, sender Sender, enq Enqueuer, mtx *metrics.Metrics) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, t *asynq.Task) error {
		var p queue.WarmupTickPayload
		if err := json.Unmarshal(t.Payload(), &p); err != nil {
			return err
		}
		job, err := core.GetWarmupSendJob(ctx, p.MailboxID, p.WorkspaceID)
		if err != nil {
			return err
		}
		// Decrypted secrets: wipe both after use, like every other worker secret.
		defer zeroize(job.SMTPPassword)
		defer zeroize(job.AccessToken)

		// Nothing to do: mailbox paused / over today's target / disabled / no
		// eligible partner. The lazy chain is intentionally NOT continued here —
		// the sweep re-seeds this mailbox once it is due again.
		// result=skipped: nothing was actionable — same bucket as ClaimSkip below.
		if job.Skip {
			mtx.SendFinalized(sendKind, "skipped")
			return nil
		}

		// scheduleNext resolves the from-mailbox's assigned worker (per-IP routing,
		// §15: a mailbox's warmup and campaign mail share one egress identity) and
		// enqueues its next tick. Shared by the already-sent, success, and
		// permanent-failure paths.
		scheduleNext := func() error {
			due, sendNow, err := core.NextWarmupDue(ctx, p.MailboxID, p.WorkspaceID)
			if err != nil {
				return err
			}
			if sendNow {
				due = time.Now()
			}
			dest, err := core.AssignMailboxWorker(ctx, p.MailboxID, p.WorkspaceID)
			if err != nil {
				return err
			}
			return enq.EnqueueWarmupTickAt(p.MailboxID, p.WorkspaceID, due, dest)
		}

		// Claim-before-send: the warmup_sends row is the delivery claim.
		//   - ClaimSkip: another worker owns a fresh 'sending' lease, or the row is
		//     terminal — nothing to do.
		//   - ClaimAlreadySent: a prior run delivered THIS exact send but its next
		//     tick didn't schedule. Recover-forward: schedule only, never re-send.
		//   - ClaimWon: we own the claim; build + send below.
		outcome, err := core.ClaimWarmupSend(ctx, job)
		if err != nil {
			return err
		}
		switch outcome {
		case coreapi.ClaimSkip:
			// result=skipped: another worker already owns this row, or it's
			// terminal — same "nothing to attempt" bucket as job.Skip above.
			mtx.SendFinalized(sendKind, "skipped")
			return nil
		case coreapi.ClaimAlreadySent:
			// No metric here: this recovers a delivery ALREADY counted "sent"
			// on the run that actually sent it (see sendErr == nil below) —
			// scheduling the next tick now must not double-count it.
			return scheduleNext()
		}

		msgID, sendErr := sender.Send(ctx,
			mail.OutboundJob{
				Provider: job.Provider, Host: job.SMTPHost, Port: job.SMTPPort,
				Username: job.SMTPUsername, Password: string(job.SMTPPassword),
				AllowPlaintext: job.AllowPlaintext, AccessToken: string(job.AccessToken),
			},
			mail.Message{
				FromEmail: job.FromEmail, FromName: job.FromName, To: job.ToEmail,
				Subject: job.Subject, BodyText: job.BodyText, BodyHTML: job.BodyHTML,
				InReplyTo: job.InReplyTo, References: job.References,
				ExtraHeaders: map[string]string{warmupHeader: job.Token},
			},
		)

		switch {
		case sendErr == nil:
			// Durable-delivered + recover-forward: finalize 'sent' FIRST so the
			// delivery is durable on its own, THEN schedule the next tick. If the
			// schedule fails, the asynq retry's claim sees the 'sent' row
			// (ClaimAlreadySent) and schedules without re-sending.
			if err := core.MarkWarmupSent(ctx, job, msgID); err != nil {
				// No metric: the delivery is not yet durable (still 'sending'),
				// so this attempt is not "sent" — the retry that eventually
				// commits records it instead.
				return fmt.Errorf("mark warmup sent: %w", err)
			}
			// result=sent, recorded now (not before MarkWarmupSent) so a crash
			// before this line leaves the retry to record it, never twice.
			mtx.SendFinalized(sendKind, "sent")
			return scheduleNext()
		case mail.Retryable(sendErr):
			// result=failed: this attempt failed, even though asynq will retry
			// the task — inroad_sends_total counts attempts, not final outcomes
			// across retries, matching the permanent-failure branch below.
			mtx.SendFinalized(sendKind, "failed")
			// Transient failure (nothing delivered): release the claim so the asynq
			// retry reclaims it, and return the error so asynq retries.
			if rerr := core.ReleaseWarmupSend(ctx, job); rerr != nil {
				return fmt.Errorf("release warmup send after transient failure: %w", rerr)
			}
			return sendErr
		default:
			// result=failed.
			mtx.SendFinalized(sendKind, "failed")
			// Permanent failure: finalize 'failed' (no thread advance) then keep the
			// mailbox's chain alive so one bad send doesn't wedge its warmup.
			if ferr := core.FailWarmupSend(ctx, job, sendErr.Error()); ferr != nil {
				return ferr
			}
			return scheduleNext()
		}
	}
}

// zeroize overwrites a decrypted secret in place after use.
func zeroize(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
