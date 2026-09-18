package inprocess

import (
	"context"
	"time"

	"github.com/inroad/inroad/internal/coreapi"
)

// OutcomeSource accepts the CLAIM AND OUTCOME path: everything a per-message
// handler writes back about the work it was handed — the claim, the delivery,
// the cursor advance, the release, the finalize, the stop, the deferral, the
// warmup finalizers, the inbox reply/bounce outcomes and the webhook delivery
// outcomes.
//
// It is the seam the coreapi REMOTE transport plugs into
// (internal/coreapi/remote, slice 3 of the plane split), exactly as JobSource is
// for slice 2's reads and SuppressionSource for slice 1's one boolean.
//
// Default is localOutcomes — the pool-backed writes every self-hosted
// installation runs, unchanged. A fleet worker replaces the source with an HTTP
// call to the control plane and then none of these twenty methods needs a
// database.
//
// ONE interface, not twenty, for the reason JobSource is one rather than eight:
// a source field and an option per method would be twenty things to wire at a
// composition root and twenty things to forget, for a set that is always
// installed together, comes from one client, and represents one decision — "this
// worker reports its work to the control plane".
//
// Reading them as a group is also the point rather than an accident. These are
// the writes the claim-before-send protocol is MADE OF, and half of them is not
// a smaller protocol: a claim with no release is a lease nothing can give back,
// and a delivery with no cursor advance is the recover-forward case that never
// recovers. Splitting this set across two slices would have been the one way to
// ship a genuinely broken intermediate state.
//
// Signature note: ids are STRINGS, matching the coreapi seam, so each
// implementation parses them itself and a malformed id is an error on both
// transports rather than an error on one and a query on the other. The three
// reply outcomes additionally accept an EMPTY enrollment id, which means the
// matched send had no enrollment; both implementations treat it as a no-op (for
// the stop — MarkUnsubscribed still suppresses).
type OutcomeSource interface {
	ClaimStepSend(ctx context.Context, job coreapi.StepSendJob) (coreapi.ClaimOutcome, error)
	MarkStepDelivered(ctx context.Context, job coreapi.StepSendJob, messageID string) error
	AdvanceStepCursor(ctx context.Context, job coreapi.StepSendJob) (coreapi.Advance, error)
	ReleaseStepSend(ctx context.Context, job coreapi.StepSendJob) error
	FinalizeStepSend(ctx context.Context, job coreapi.StepSendJob, res coreapi.StepResult) (coreapi.Advance, error)
	MarkStepStopped(ctx context.Context, enrollmentID, workspaceID, reason string) error
	DeferEnrollment(ctx context.Context, enrollmentID, workspaceID string, until time.Time) error
	IncrementEnrollmentCapDeferrals(ctx context.Context, enrollmentID, workspaceID string) (int, error)

	ClaimWarmupSend(ctx context.Context, job coreapi.WarmupSendJob) (coreapi.ClaimOutcome, error)
	MarkWarmupSent(ctx context.Context, job coreapi.WarmupSendJob, messageID string) error
	ReleaseWarmupSend(ctx context.Context, job coreapi.WarmupSendJob) error
	FailWarmupSend(ctx context.Context, job coreapi.WarmupSendJob, errMsg string) error
	MarkWarmupEngaged(ctx context.Context, receiptID, workspaceID string, replied bool) error

	MarkReplied(ctx context.Context, enrollmentID, workspaceID, replyClass, replySource string, confidence float64) error
	RecordReplyClass(ctx context.Context, enrollmentID, workspaceID, class, source string, confidence float64) error
	MarkUnsubscribed(ctx context.Context, enrollmentID, workspaceID, email string) error
	MarkBounced(ctx context.Context, enrollmentID, workspaceID, email string, hard bool) error

	MarkWebhookDelivered(ctx context.Context, deliveryID, workspaceID string, attempts, responseStatus int) error
	MarkWebhookRetrying(ctx context.Context, deliveryID, workspaceID string, attempts int, lastErr string, responseStatus *int, nextAttemptAt time.Time) error
	MarkWebhookFailed(ctx context.Context, deliveryID, workspaceID string, attempts int, lastErr string, responseStatus *int) error
}

// localOutcomes is the in-process source: the SAME writes that shipped before
// this seam existed, reached through the client that holds the pool.
//
// It holds the client by VALUE and is built fresh in outcomeSource() rather than
// stored at construction, for the reason localJobs does: a stored copy would be
// a copy taken at one instant, and every reader would have to satisfy themselves
// it was taken after the options had been applied.
type localOutcomes struct{ c client }

var _ OutcomeSource = localOutcomes{}

func (l localOutcomes) ClaimStepSend(ctx context.Context, job coreapi.StepSendJob) (coreapi.ClaimOutcome, error) {
	return l.c.localClaimStepSend(ctx, job)
}

func (l localOutcomes) MarkStepDelivered(ctx context.Context, job coreapi.StepSendJob, messageID string) error {
	return l.c.localMarkStepDelivered(ctx, job, messageID)
}

func (l localOutcomes) AdvanceStepCursor(ctx context.Context, job coreapi.StepSendJob) (coreapi.Advance, error) {
	return l.c.localAdvanceStepCursor(ctx, job)
}

func (l localOutcomes) ReleaseStepSend(ctx context.Context, job coreapi.StepSendJob) error {
	return l.c.localReleaseStepSend(ctx, job)
}

func (l localOutcomes) FinalizeStepSend(ctx context.Context, job coreapi.StepSendJob, res coreapi.StepResult) (coreapi.Advance, error) {
	return l.c.localFinalizeStepSend(ctx, job, res)
}

func (l localOutcomes) MarkStepStopped(ctx context.Context, enrollmentID, workspaceID, reason string) error {
	return l.c.localMarkStepStopped(ctx, enrollmentID, workspaceID, reason)
}

func (l localOutcomes) DeferEnrollment(ctx context.Context, enrollmentID, workspaceID string, until time.Time) error {
	return l.c.localDeferEnrollment(ctx, enrollmentID, workspaceID, until)
}

func (l localOutcomes) IncrementEnrollmentCapDeferrals(ctx context.Context, enrollmentID, workspaceID string) (int, error) {
	return l.c.localIncrementEnrollmentCapDeferrals(ctx, enrollmentID, workspaceID)
}

func (l localOutcomes) ClaimWarmupSend(ctx context.Context, job coreapi.WarmupSendJob) (coreapi.ClaimOutcome, error) {
	return l.c.localClaimWarmupSend(ctx, job)
}

func (l localOutcomes) MarkWarmupSent(ctx context.Context, job coreapi.WarmupSendJob, messageID string) error {
	return l.c.localMarkWarmupSent(ctx, job, messageID)
}

func (l localOutcomes) ReleaseWarmupSend(ctx context.Context, job coreapi.WarmupSendJob) error {
	return l.c.localReleaseWarmupSend(ctx, job)
}

func (l localOutcomes) FailWarmupSend(ctx context.Context, job coreapi.WarmupSendJob, errMsg string) error {
	return l.c.localFailWarmupSend(ctx, job, errMsg)
}

func (l localOutcomes) MarkWarmupEngaged(ctx context.Context, receiptID, workspaceID string, replied bool) error {
	return l.c.localMarkWarmupEngaged(ctx, receiptID, workspaceID, replied)
}

func (l localOutcomes) MarkReplied(ctx context.Context, enrollmentID, workspaceID, replyClass, replySource string, confidence float64) error {
	return l.c.localMarkReplied(ctx, enrollmentID, workspaceID, replyClass, replySource, confidence)
}

func (l localOutcomes) RecordReplyClass(ctx context.Context, enrollmentID, workspaceID, class, source string, confidence float64) error {
	return l.c.localRecordReplyClass(ctx, enrollmentID, workspaceID, class, source, confidence)
}

func (l localOutcomes) MarkUnsubscribed(ctx context.Context, enrollmentID, workspaceID, email string) error {
	return l.c.localMarkUnsubscribed(ctx, enrollmentID, workspaceID, email)
}

func (l localOutcomes) MarkBounced(ctx context.Context, enrollmentID, workspaceID, email string, hard bool) error {
	return l.c.localMarkBounced(ctx, enrollmentID, workspaceID, email, hard)
}

func (l localOutcomes) MarkWebhookDelivered(ctx context.Context, deliveryID, workspaceID string, attempts, responseStatus int) error {
	return l.c.localMarkWebhookDelivered(ctx, deliveryID, workspaceID, attempts, responseStatus)
}

func (l localOutcomes) MarkWebhookRetrying(ctx context.Context, deliveryID, workspaceID string, attempts int, lastErr string, responseStatus *int, nextAttemptAt time.Time) error {
	return l.c.localMarkWebhookRetrying(ctx, deliveryID, workspaceID, attempts, lastErr, responseStatus, nextAttemptAt)
}

func (l localOutcomes) MarkWebhookFailed(ctx context.Context, deliveryID, workspaceID string, attempts int, lastErr string, responseStatus *int) error {
	return l.c.localMarkWebhookFailed(ctx, deliveryID, workspaceID, attempts, lastErr, responseStatus)
}

// outcomeSource resolves where this client writes outcomes. A nil c.outcomes
// means "here, to the pool", which is the self-host path and what a worker that
// sets no new variable gets. Same nil-means-local shape as jobSource, and for
// the same reason: these writes need the whole client — the enrollment state
// machine, the realtime publisher, the webhook emitter, the metrics — so a
// default installed in New would have to capture a copy of the client
// mid-construction.
func (c client) outcomeSource() OutcomeSource {
	if c.outcomes != nil {
		return c.outcomes
	}
	return localOutcomes{c: c}
}

// The exported methods, each one line. They exist so every consumer of an
// outcome write goes through the ONE field above — swapping it moves them all at
// once and there is no second path to forget, which is the property that made
// slice 1's SuppressionSource worth having.

func (c client) ClaimStepSend(ctx context.Context, job coreapi.StepSendJob) (coreapi.ClaimOutcome, error) {
	return c.outcomeSource().ClaimStepSend(ctx, job)
}

func (c client) MarkStepDelivered(ctx context.Context, job coreapi.StepSendJob, messageID string) error {
	return c.outcomeSource().MarkStepDelivered(ctx, job, messageID)
}

func (c client) AdvanceStepCursor(ctx context.Context, job coreapi.StepSendJob) (coreapi.Advance, error) {
	return c.outcomeSource().AdvanceStepCursor(ctx, job)
}

func (c client) ReleaseStepSend(ctx context.Context, job coreapi.StepSendJob) error {
	return c.outcomeSource().ReleaseStepSend(ctx, job)
}

func (c client) FinalizeStepSend(ctx context.Context, job coreapi.StepSendJob, res coreapi.StepResult) (coreapi.Advance, error) {
	return c.outcomeSource().FinalizeStepSend(ctx, job, res)
}

func (c client) MarkStepStopped(ctx context.Context, enrollmentID, workspaceID, reason string) error {
	return c.outcomeSource().MarkStepStopped(ctx, enrollmentID, workspaceID, reason)
}

func (c client) DeferEnrollment(ctx context.Context, enrollmentID, workspaceID string, until time.Time) error {
	return c.outcomeSource().DeferEnrollment(ctx, enrollmentID, workspaceID, until)
}

func (c client) IncrementEnrollmentCapDeferrals(ctx context.Context, enrollmentID, workspaceID string) (int, error) {
	return c.outcomeSource().IncrementEnrollmentCapDeferrals(ctx, enrollmentID, workspaceID)
}

func (c client) ClaimWarmupSend(ctx context.Context, job coreapi.WarmupSendJob) (coreapi.ClaimOutcome, error) {
	return c.outcomeSource().ClaimWarmupSend(ctx, job)
}

func (c client) MarkWarmupSent(ctx context.Context, job coreapi.WarmupSendJob, messageID string) error {
	return c.outcomeSource().MarkWarmupSent(ctx, job, messageID)
}

func (c client) ReleaseWarmupSend(ctx context.Context, job coreapi.WarmupSendJob) error {
	return c.outcomeSource().ReleaseWarmupSend(ctx, job)
}

func (c client) FailWarmupSend(ctx context.Context, job coreapi.WarmupSendJob, errMsg string) error {
	return c.outcomeSource().FailWarmupSend(ctx, job, errMsg)
}

func (c client) MarkWarmupEngaged(ctx context.Context, receiptID, workspaceID string, replied bool) error {
	return c.outcomeSource().MarkWarmupEngaged(ctx, receiptID, workspaceID, replied)
}

func (c client) MarkReplied(ctx context.Context, enrollmentID, workspaceID, replyClass, replySource string, confidence float64) error {
	return c.outcomeSource().MarkReplied(ctx, enrollmentID, workspaceID, replyClass, replySource, confidence)
}

func (c client) RecordReplyClass(ctx context.Context, enrollmentID, workspaceID, class, source string, confidence float64) error {
	return c.outcomeSource().RecordReplyClass(ctx, enrollmentID, workspaceID, class, source, confidence)
}

func (c client) MarkUnsubscribed(ctx context.Context, enrollmentID, workspaceID, email string) error {
	return c.outcomeSource().MarkUnsubscribed(ctx, enrollmentID, workspaceID, email)
}

func (c client) MarkBounced(ctx context.Context, enrollmentID, workspaceID, email string, hard bool) error {
	return c.outcomeSource().MarkBounced(ctx, enrollmentID, workspaceID, email, hard)
}

func (c client) MarkWebhookDelivered(ctx context.Context, deliveryID, workspaceID string, attempts, responseStatus int) error {
	return c.outcomeSource().MarkWebhookDelivered(ctx, deliveryID, workspaceID, attempts, responseStatus)
}

func (c client) MarkWebhookRetrying(ctx context.Context, deliveryID, workspaceID string, attempts int, lastErr string, responseStatus *int, nextAttemptAt time.Time) error {
	return c.outcomeSource().MarkWebhookRetrying(ctx, deliveryID, workspaceID, attempts, lastErr, responseStatus, nextAttemptAt)
}

func (c client) MarkWebhookFailed(ctx context.Context, deliveryID, workspaceID string, attempts int, lastErr string, responseStatus *int) error {
	return c.outcomeSource().MarkWebhookFailed(ctx, deliveryID, workspaceID, attempts, lastErr, responseStatus)
}
