package remote

import (
	"context"
	"fmt"
	"time"

	"github.com/inroad/inroad/internal/coreapi"
)

// The CLAIM AND OUTCOME path, slice 3 of the plane split: everything that
// claims, marks, finalizes, advances, stops, defers or fails.
//
// # What is different about writing
//
// Slices 1 and 2 moved READS. A read that fails is answered with an error and a
// zero value, the caller refuses to send, and nothing in the database changed.
// A WRITE has a third outcome a function call does not: the request arrived, the
// control plane committed, and the response was lost. The worker cannot tell
// that apart from "the call never happened", and it must be safe under both
// readings.
//
// Nothing in this file tries to tell them apart. Every method fails the CALL and
// lets asynq retry the task, and the retry passes through the CLAIM, which is
// the only thing in the system that knows what already happened. That is the
// whole design: the transport is allowed to be uncertain because the claim is
// not.
//
// # The idempotency audit, method by method
//
// The rule is idempotent by KEY (a deterministic send id, an enrollment id, a
// receipt id, a delivery id), never by attempt. Every method below was audited
// against its SQL rather than assumed:
//
//   - ClaimStepSend / ClaimWarmupSend — idempotent by the deterministic send id,
//     which IS the row id. A repeat from the worker that already won finds its
//     own fresh lease, loses, and is told ClaimSkip; the sweeper re-drives it
//     once the lease expires. That is exactly what an in-process worker that
//     crashed immediately after committing its claim does.
//   - MarkStepDelivered — SetSendResult sets status/message_id ABSOLUTELY,
//     workspace-pinned. A repeat writes the same values. (It also re-stamps
//     sent_at, which is the one value that moves; nothing branches on it, and
//     the claim means the retry takes ClaimAlreadySent and never calls this
//     twice anyway.)
//   - AdvanceStepCursor — current_step is set absolutely, so a re-advance lands
//     on the same value. This is the documented recover-forward path and was
//     already idempotent because it had to be.
//   - FinalizeStepSend — the finalize and the advance commit in ONE transaction,
//     both absolute. A repeat re-writes the same terminal state.
//   - ReleaseStepSend / ReleaseWarmupSend — guarded on status='sending' in SQL.
//     A repeat matches zero rows.
//   - MarkStepStopped — the enrollment state machine's stop is guarded on
//     status='active'. A repeat matches zero rows.
//   - DeferEnrollment — sets next_due_at to the caller's absolute instant,
//     guarded on 'active'. A repeat writes the same instant.
//   - MarkWarmupSent — the finalize is guarded on 'sending' and returns rows
//     affected; zero rows skips the thread advance and the daily-stat bump, so a
//     repeat cannot double-count.
//   - FailWarmupSend — guarded on 'sending'.
//   - MarkWarmupEngaged — guarded on NOT engaged, and the reply-counter bump is
//     inside that guard's transaction.
//   - MarkReplied / RecordReplyClass — the class/source/confidence are set
//     absolutely; the stop is guarded on 'active'.
//   - MarkUnsubscribed / MarkBounced — the suppression insert is
//     ON CONFLICT DO NOTHING, and the stop is guarded on 'active'.
//   - MarkWebhookDelivered / Retrying / Failed — all three are guarded on
//     status='pending' and set attempts absolutely from the caller's count.
//
// # The one exception, and why it is left alone
//
// IncrementEnrollmentCapDeferrals is `cap_deferrals = cap_deferrals + 1
// RETURNING`, so a lost response means the counter is bumped twice for one
// deferral. It is left non-idempotent deliberately:
//
//   - The over-count changes NOTHING except when a log line is emitted. The
//     count-based ceiling that used to stop an enrollment was removed precisely
//     because a daily cap is self-clearing (see the deferForCapacity comment in
//     internal/worker/sequence/advance.go); crossing it now WARNs.
//   - The window is not new. In process, the counter is bumped and then
//     EnqueueAdvanceIn runs; an enqueue failure returns an error, asynq
//     redelivers, and the counter is bumped again. This transport adds a second
//     window of identical size and identical consequence.
//   - Closing it would mean an attempt id on the wire, which means changing the
//     coreapi signature — and therefore every fake and every caller — to make a
//     diagnostic counter exact. That is not a trade worth making, and pretending
//     otherwise by adding an idempotency key here would be a new table for a log
//     threshold.
//
// # Ids in, values out — and the one place values go in
//
// The claim/mark/finalize methods take the JOB the control plane built and
// handed to this worker, so the job goes back on the request. See wire.go for
// why it travels whole rather than as a subset, and for what a worker can and
// cannot do with that. Every other route here carries ids and small scalars.

// ClaimStepSend claims one step-send for delivery.
//
// FAIL CLOSED, and note what that means for a CLAIM specifically: a failed call
// returns (ClaimSkip, err), and the caller returns the error so asynq retries.
// ClaimSkip is the zero value and means "do nothing", so a caller that ignored
// the error would fail safe rather than send — but no caller does, and the
// pairing is deliberate rather than accidental.
//
// What it must NEVER do is invent a ClaimWon on a failed call. That would be a
// send authorised by a claim nobody took, which is the double send this slice
// exists to prevent.
//
// # Which clock decides "not yet due"
//
// The claim's not-due gate compares job.NotDueUntil — sequence_enrollments.
// next_due_at, stamped by the DATABASE's clock — against the database's own
// now(), just before the claim transaction (queries/stepsend.sql StepSendNotYetDue).
// No process clock takes part, so neither the fleet host running this client
// nor the control plane serving the route can turn a due-now enrollment into
// ClaimDeferred by being behind the database. (It used to compare against the
// control plane's time.Now(), which moved the problem off the fleet host but
// left it between the control plane and its database.)
func (c *Client) ClaimStepSend(ctx context.Context, job coreapi.StepSendJob) (coreapi.ClaimOutcome, error) {
	if err := parseIDs(job.WorkspaceID); err != nil {
		return coreapi.ClaimSkip, err
	}
	var out claimOutcomeResponse
	if err := c.post(ctx, c.outcomes, PathStepSendClaim, stepJobRequest{
		WorkspaceID: job.WorkspaceID, Job: job,
	}, &out); err != nil {
		return coreapi.ClaimSkip, err
	}
	return decodeClaimOutcome(PathStepSendClaim, out.Outcome)
}

// MarkStepDelivered records a successful delivery in its own committed
// statement, BEFORE AdvanceStepCursor.
//
// The ordering is the load-bearing part and it survives the wire unchanged:
// these are two calls, so the second can fail independently, and when it does
// the asynq retry's claim sees the 'sent' row and recovers forward. A
// transport that merged them into one call to save a round trip would delete
// the recovery.
func (c *Client) MarkStepDelivered(ctx context.Context, job coreapi.StepSendJob, messageID string) error {
	if err := parseIDs(job.WorkspaceID); err != nil {
		return err
	}
	return c.post(ctx, c.outcomes, PathStepSendDelivered, stepDeliveredRequest{
		WorkspaceID: job.WorkspaceID, Job: job, MessageID: messageID,
	}, &ackResponse{})
}

// AdvanceStepCursor advances the enrollment cursor for the just-delivered step.
// Idempotent, so this is also the recover-forward call a ClaimAlreadySent
// retry makes.
func (c *Client) AdvanceStepCursor(ctx context.Context, job coreapi.StepSendJob) (coreapi.Advance, error) {
	if err := parseIDs(job.WorkspaceID); err != nil {
		return coreapi.Advance{}, err
	}
	var out advanceResponse
	if err := c.post(ctx, c.outcomes, PathStepSendAdvance, stepJobRequest{
		WorkspaceID: job.WorkspaceID, Job: job,
	}, &out); err != nil {
		return coreapi.Advance{}, err
	}
	return out.Advance, nil
}

// ReleaseStepSend expires a claim's lease after a RETRYABLE failure so the
// asynq retry reclaims promptly.
//
// A failed release is not a dropped send: the lease still expires on its own,
// and the sweeper re-drives the enrollment. It costs the retry a wait, which is
// why the caller reports the failure rather than swallowing it.
func (c *Client) ReleaseStepSend(ctx context.Context, job coreapi.StepSendJob) error {
	if err := parseIDs(job.WorkspaceID); err != nil {
		return err
	}
	return c.post(ctx, c.outcomes, PathStepSendRelease, stepJobRequest{
		WorkspaceID: job.WorkspaceID, Job: job,
	}, &ackResponse{})
}

// FinalizeStepSend finalizes a claimed step to 'failed' AND advances the cursor
// in one transaction (fail-forward). The SUCCESS path does not use it.
func (c *Client) FinalizeStepSend(ctx context.Context, job coreapi.StepSendJob, res coreapi.StepResult) (coreapi.Advance, error) {
	if err := parseIDs(job.WorkspaceID); err != nil {
		return coreapi.Advance{}, err
	}
	var out advanceResponse
	if err := c.post(ctx, c.outcomes, PathStepSendFinalize, stepFinalizeRequest{
		WorkspaceID: job.WorkspaceID, Job: job, Result: res,
	}, &out); err != nil {
		return coreapi.Advance{}, err
	}
	return out.Advance, nil
}

// MarkStepStopped halts an enrollment through the single stop entry point.
func (c *Client) MarkStepStopped(ctx context.Context, enrollmentID, workspaceID, reason string) error {
	if err := parseIDs(workspaceID, enrollmentID); err != nil {
		return err
	}
	// reason travels UNVALIDATED, like every other free-text value on this
	// transport: the enrollment state machine is what rejects an unknown stop
	// reason, in process and here, and a second check on one side only would be
	// a behaviour difference between the two.
	return c.post(ctx, c.outcomes, PathEnrollmentStop, enrollmentStopRequest{
		WorkspaceID: workspaceID, EnrollmentID: enrollmentID, Reason: reason,
	}, &ackResponse{})
}

// DeferEnrollment pushes an active enrollment's next_due_at out to until.
func (c *Client) DeferEnrollment(ctx context.Context, enrollmentID, workspaceID string, until time.Time) error {
	if err := parseIDs(workspaceID, enrollmentID); err != nil {
		return err
	}
	return c.post(ctx, c.outcomes, PathEnrollmentDefer, enrollmentDeferRequest{
		WorkspaceID: workspaceID, EnrollmentID: enrollmentID, Until: until,
	}, &ackResponse{})
}

// IncrementEnrollmentCapDeferrals bumps the cap-deferral counter and returns the
// new value. The ONE method here that is not idempotent — see this file's audit
// for why that is left as it is rather than papered over.
func (c *Client) IncrementEnrollmentCapDeferrals(ctx context.Context, enrollmentID, workspaceID string) (int, error) {
	if err := parseIDs(workspaceID, enrollmentID); err != nil {
		return 0, err
	}
	var out capDeferralResponse
	if err := c.post(ctx, c.outcomes, PathEnrollmentCapDeferral, enrollmentRequest{
		WorkspaceID: workspaceID, EnrollmentID: enrollmentID,
	}, &out); err != nil {
		return 0, err
	}
	return out.Deferrals, nil
}

// ClaimWarmupSend claims one warmup send, same four-state protocol and same
// fail-closed pairing as ClaimStepSend.
func (c *Client) ClaimWarmupSend(ctx context.Context, job coreapi.WarmupSendJob) (coreapi.ClaimOutcome, error) {
	if err := parseIDs(job.WorkspaceID); err != nil {
		return coreapi.ClaimSkip, err
	}
	var out claimOutcomeResponse
	if err := c.post(ctx, c.outcomes, PathWarmupSendClaim, warmupJobRequest{
		WorkspaceID: job.WorkspaceID, Job: job,
	}, &out); err != nil {
		return coreapi.ClaimSkip, err
	}
	return decodeClaimOutcome(PathWarmupSendClaim, out.Outcome)
}

// MarkWarmupSent finalizes a claimed warmup send to 'sent' and advances the
// thread, in one transaction on the control plane.
func (c *Client) MarkWarmupSent(ctx context.Context, job coreapi.WarmupSendJob, messageID string) error {
	if err := parseIDs(job.WorkspaceID); err != nil {
		return err
	}
	return c.post(ctx, c.outcomes, PathWarmupSendSent, warmupSentRequest{
		WorkspaceID: job.WorkspaceID, Job: job, MessageID: messageID,
	}, &ackResponse{})
}

// ReleaseWarmupSend returns a claimed row to 'queued' after a RETRYABLE failure.
func (c *Client) ReleaseWarmupSend(ctx context.Context, job coreapi.WarmupSendJob) error {
	if err := parseIDs(job.WorkspaceID); err != nil {
		return err
	}
	return c.post(ctx, c.outcomes, PathWarmupSendRelease, warmupJobRequest{
		WorkspaceID: job.WorkspaceID, Job: job,
	}, &ackResponse{})
}

// FailWarmupSend finalizes a claimed row to 'failed' after a PERMANENT failure.
func (c *Client) FailWarmupSend(ctx context.Context, job coreapi.WarmupSendJob, errMsg string) error {
	if err := parseIDs(job.WorkspaceID); err != nil {
		return err
	}
	return c.post(ctx, c.outcomes, PathWarmupSendFail, warmupFailRequest{
		WorkspaceID: job.WorkspaceID, Job: job, Error: errMsg,
	}, &ackResponse{})
}

// MarkWarmupEngaged flips one receipt's engaged guard.
func (c *Client) MarkWarmupEngaged(ctx context.Context, receiptID, workspaceID string, replied bool) error {
	if err := parseIDs(workspaceID, receiptID); err != nil {
		return err
	}
	return c.post(ctx, c.outcomes, PathWarmupEngaged, warmupEngagedRequest{
		WorkspaceID: workspaceID, ReceiptID: receiptID, Replied: replied,
	}, &ackResponse{})
}

// MarkReplied halts the enrollment on an inbound reply and tags it.
//
// An EMPTY enrollmentID is legal and means the matched send has no enrollment
// (the legacy direct-send path). It is passed through rather than rejected,
// because in process it makes the method a no-op — and a 400 here would be an
// error the poller cannot tell from a real failure, so it would return before
// SetInboxCursor and stop the mailbox processing ALL inbound mail.
func (c *Client) MarkReplied(ctx context.Context, enrollmentID, workspaceID, replyClass, replySource string, confidence float64) error {
	return c.postReplyClass(ctx, PathReplyReplied, enrollmentID, workspaceID, replyClass, replySource, confidence)
}

// RecordReplyClass tags the enrollment WITHOUT stopping it — the automated-reply
// path. Same empty-enrollment rule as MarkReplied.
func (c *Client) RecordReplyClass(ctx context.Context, enrollmentID, workspaceID, class, source string, confidence float64) error {
	return c.postReplyClass(ctx, PathReplyClass, enrollmentID, workspaceID, class, source, confidence)
}

// postReplyClass is the shared half of the two routes above. They differ only in
// which one stops the enrollment, and that is decided by the PATH — so the
// request assembly lives once, where the empty-enrollment rule can be right
// once.
func (c *Client) postReplyClass(ctx context.Context, path, enrollmentID, workspaceID, class, source string, confidence float64) error {
	if err := parseIDs(workspaceID); err != nil {
		return err
	}
	if err := parseOptionalID(enrollmentID); err != nil {
		return err
	}
	return c.post(ctx, c.outcomes, path, replyClassRequest{
		WorkspaceID: workspaceID, EnrollmentID: enrollmentID,
		Class: class, Source: source, Confidence: confidence,
	}, &ackResponse{})
}

// MarkUnsubscribed suppresses the address and, when an enrollment matched, stops
// it. The suppression is the load-bearing write and happens EVEN WHEN
// enrollmentID is "" — compliance, docs/security.md invariant 20 — so the empty
// id travels rather than being refused.
func (c *Client) MarkUnsubscribed(ctx context.Context, enrollmentID, workspaceID, email string) error {
	if err := parseIDs(workspaceID); err != nil {
		return err
	}
	if err := parseOptionalID(enrollmentID); err != nil {
		return err
	}
	return c.post(ctx, c.outcomes, PathReplyUnsubscribed, unsubscribeRequest{
		WorkspaceID: workspaceID, EnrollmentID: enrollmentID, Email: email,
	}, &ackResponse{})
}

// MarkBounced records a hard bounce: stops the enrollment (if any) and
// suppresses the address.
//
// hard=false travels rather than being short-circuited here. In process it is a
// no-op, and a transport that decided not to make the call would be a behaviour
// difference — the one thing a transport must not introduce — for a case the
// caller does not actually produce.
func (c *Client) MarkBounced(ctx context.Context, enrollmentID, workspaceID, email string, hard bool) error {
	if err := parseIDs(workspaceID); err != nil {
		return err
	}
	if err := parseOptionalID(enrollmentID); err != nil {
		return err
	}
	return c.post(ctx, c.outcomes, PathReplyBounced, bounceRequest{
		WorkspaceID: workspaceID, EnrollmentID: enrollmentID, Email: email, Hard: hard,
	}, &ackResponse{})
}

// MarkWebhookDelivered finalizes a delivery to 'delivered'.
func (c *Client) MarkWebhookDelivered(ctx context.Context, deliveryID, workspaceID string, attempts, responseStatus int) error {
	if err := parseIDs(workspaceID, deliveryID); err != nil {
		return err
	}
	return c.post(ctx, c.outcomes, PathWebhookMarkDelivered, webhookDeliveredRequest{
		WorkspaceID: workspaceID, DeliveryID: deliveryID,
		Attempts: attempts, ResponseStatus: responseStatus,
	}, &ackResponse{})
}

// MarkWebhookRetrying records a failed attempt that still has retries left.
func (c *Client) MarkWebhookRetrying(ctx context.Context, deliveryID, workspaceID string, attempts int, lastErr string, responseStatus *int, nextAttemptAt time.Time) error {
	if err := parseIDs(workspaceID, deliveryID); err != nil {
		return err
	}
	return c.post(ctx, c.outcomes, PathWebhookMarkRetrying, webhookRetryingRequest{
		WorkspaceID: workspaceID, DeliveryID: deliveryID, Attempts: attempts,
		LastError: lastErr, ResponseStatus: responseStatus, NextAttemptAt: nextAttemptAt,
	}, &ackResponse{})
}

// MarkWebhookFailed finalizes a delivery to 'failed' after the retry schedule is
// exhausted.
func (c *Client) MarkWebhookFailed(ctx context.Context, deliveryID, workspaceID string, attempts int, lastErr string, responseStatus *int) error {
	if err := parseIDs(workspaceID, deliveryID); err != nil {
		return err
	}
	return c.post(ctx, c.outcomes, PathWebhookMarkFailed, webhookFailedRequest{
		WorkspaceID: workspaceID, DeliveryID: deliveryID, Attempts: attempts,
		LastError: lastErr, ResponseStatus: responseStatus,
	}, &ackResponse{})
}

// parseOptionalID accepts the empty string — three inbox outcomes take an
// enrollment id that is legitimately absent — and rejects anything else that is
// not a uuid. It is deliberately NOT parseIDs with a special case: "empty is
// allowed here" is a property of four specific call sites, and burying it in the
// shared helper would silently let every other route accept one.
func parseOptionalID(id string) error {
	if id == "" {
		return nil
	}
	return parseIDs(id)
}

// encodeClaimOutcome renders the four-state protocol for the wire. An outcome
// this vocabulary does not carry is an ERROR rather than a default: it can only
// mean the enum grew a state the transport was not taught, and answering "skip"
// would turn that into a fleet that quietly stops sending.
func encodeClaimOutcome(o coreapi.ClaimOutcome) (string, error) {
	switch o {
	case coreapi.ClaimSkip:
		return claimOutcomeSkip, nil
	case coreapi.ClaimWon:
		return claimOutcomeWon, nil
	case coreapi.ClaimAlreadySent:
		return claimOutcomeAlreadySent, nil
	case coreapi.ClaimDeferred:
		return claimOutcomeDeferred, nil
	default:
		return "", fmt.Errorf("coreapi remote: unknown claim outcome %d", int(o))
	}
}

// decodeClaimOutcome is the reading half, and the one that must never guess: an
// unrecognised value returns ClaimSkip ALONGSIDE an error, so a caller that
// (wrongly) ignored the error still does not send, and a caller that handles it
// retries through the claim rather than acting on a state nobody agreed on.
func decodeClaimOutcome(path, s string) (coreapi.ClaimOutcome, error) {
	switch s {
	case claimOutcomeSkip:
		return coreapi.ClaimSkip, nil
	case claimOutcomeWon:
		return coreapi.ClaimWon, nil
	case claimOutcomeAlreadySent:
		return coreapi.ClaimAlreadySent, nil
	case claimOutcomeDeferred:
		return coreapi.ClaimDeferred, nil
	default:
		// The VALUE is echoed because it is this package's own closed
		// vocabulary, not tenant data and not an upstream error string — an
		// operator debugging a version skew needs to see what arrived.
		return coreapi.ClaimSkip, fmt.Errorf("coreapi remote: %s: control plane answered with an unknown claim outcome %q", path, s)
	}
}
