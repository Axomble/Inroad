package inprocess

import (
	"context"

	"github.com/inroad/inroad/internal/coreapi"
)

// JobSource answers the per-message job READS: "what work is there to do about
// this one enrollment / mailbox / receipt / delivery". It is the seam the
// coreapi REMOTE transport plugs into (internal/coreapi/remote, slice 2 of the
// plane split), exactly as SuppressionSource is for slice 1's one boolean.
//
// Default is localJobs — the pool-backed builds every self-hosted installation
// runs, unchanged. A fleet worker replaces the source with an HTTP call to the
// control plane and then none of these eight methods needs a database.
//
// ONE interface, not eight. The alternative — a source field and an option per
// method — would be eight things to wire at a composition root and eight things
// to forget, for a set that is always installed together, comes from one
// client, and represents one decision: "this worker reads its work from the
// control plane". Slice 1 kept IsSuppressed separate because it shipped
// separately, not because the split is meaningful; both are installed from the
// same *remote.Client.
//
// Signature note: ids are STRINGS, matching the coreapi seam and the worker
// interfaces that consume these, so each implementation parses them itself and
// a malformed id is an error on both transports rather than an error on one and
// a query on the other.
type JobSource interface {
	GetStepSendJob(ctx context.Context, enrollmentID, workspaceID string) (coreapi.StepSendJob, error)
	GetInboxPollJob(ctx context.Context, mailboxID, workspaceID string) (coreapi.InboxPollJob, error)
	GetWarmupSendJob(ctx context.Context, mailboxID, workspaceID string) (coreapi.WarmupSendJob, error)
	GetWarmupEngageJob(ctx context.Context, receiptID, workspaceID string) (coreapi.WarmupEngageJob, error)
	GetWebhookDeliveryJob(ctx context.Context, deliveryID, workspaceID string) (coreapi.WebhookDeliveryJob, error)
	GetTestSendContent(ctx context.Context, workspaceID, campaignID, stepID string) (coreapi.TestSendContent, error)
	ResolveSenderTransport(ctx context.Context, workspaceID, mailboxID string) (coreapi.SenderTransport, error)
	FindSendByMessageID(ctx context.Context, workspaceID, messageID string) (coreapi.SendRef, error)
}

// localJobs is the in-process source: the SAME builds that shipped before this
// seam existed, reached through the client they need.
//
// It holds the client by VALUE and is built fresh in jobSource() rather than
// stored on the client at construction. Storing it would mean a client field
// pointing at a copy of the client — legal, but a copy taken at one instant,
// and every reader would have to satisfy themselves it was taken after the
// options had been applied. Building it from the receiver cannot be wrong.
type localJobs struct{ c client }

var _ JobSource = localJobs{}

func (l localJobs) GetStepSendJob(ctx context.Context, enrollmentID, workspaceID string) (coreapi.StepSendJob, error) {
	return l.c.localStepSendJob(ctx, enrollmentID, workspaceID)
}

func (l localJobs) GetInboxPollJob(ctx context.Context, mailboxID, workspaceID string) (coreapi.InboxPollJob, error) {
	return l.c.localInboxPollJob(ctx, mailboxID, workspaceID)
}

func (l localJobs) GetWarmupSendJob(ctx context.Context, mailboxID, workspaceID string) (coreapi.WarmupSendJob, error) {
	return l.c.localWarmupSendJob(ctx, mailboxID, workspaceID)
}

func (l localJobs) GetWarmupEngageJob(ctx context.Context, receiptID, workspaceID string) (coreapi.WarmupEngageJob, error) {
	return l.c.localWarmupEngageJob(ctx, receiptID, workspaceID)
}

func (l localJobs) GetWebhookDeliveryJob(ctx context.Context, deliveryID, workspaceID string) (coreapi.WebhookDeliveryJob, error) {
	return l.c.localWebhookDeliveryJob(ctx, deliveryID, workspaceID)
}

func (l localJobs) GetTestSendContent(ctx context.Context, workspaceID, campaignID, stepID string) (coreapi.TestSendContent, error) {
	return l.c.localTestSendContent(ctx, workspaceID, campaignID, stepID)
}

func (l localJobs) ResolveSenderTransport(ctx context.Context, workspaceID, mailboxID string) (coreapi.SenderTransport, error) {
	return l.c.localResolveSenderTransport(ctx, workspaceID, mailboxID)
}

func (l localJobs) FindSendByMessageID(ctx context.Context, workspaceID, messageID string) (coreapi.SendRef, error) {
	return l.c.localFindSendByMessageID(ctx, workspaceID, messageID)
}

// jobSource resolves where this client reads per-message jobs from. A nil
// c.jobs means "here, from the pool", which is the self-host path and what a
// worker that sets no new variable gets.
//
// Nil-means-local rather than a field New defaults, which is the one place this
// departs from SuppressionSource's shape. The reason is that localSuppression
// needs only *gen.Queries while these builds need the whole client — the
// credential opener, the enrollment service, the warmup content library, the
// clock — so a default installed in New would have to capture a copy of the
// client mid-construction. Resolving from the receiver instead is impossible to
// get wrong, and it keeps the bare client{} literals this package's unit tests
// drive working exactly as they did.
func (c client) jobSource() JobSource {
	if c.jobs != nil {
		return c.jobs
	}
	return localJobs{c: c}
}

// The exported methods, each one line. They exist so every consumer of a job
// read goes through the ONE field above — swapping it moves them all at once
// and there is no second path to forget, which is the property that made
// slice 1's SuppressionSource worth having.

func (c client) GetStepSendJob(ctx context.Context, enrollmentID, workspaceID string) (coreapi.StepSendJob, error) {
	return c.jobSource().GetStepSendJob(ctx, enrollmentID, workspaceID)
}

func (c client) GetInboxPollJob(ctx context.Context, mailboxID, workspaceID string) (coreapi.InboxPollJob, error) {
	return c.jobSource().GetInboxPollJob(ctx, mailboxID, workspaceID)
}

func (c client) GetWarmupSendJob(ctx context.Context, mailboxID, workspaceID string) (coreapi.WarmupSendJob, error) {
	return c.jobSource().GetWarmupSendJob(ctx, mailboxID, workspaceID)
}

func (c client) GetWarmupEngageJob(ctx context.Context, receiptID, workspaceID string) (coreapi.WarmupEngageJob, error) {
	return c.jobSource().GetWarmupEngageJob(ctx, receiptID, workspaceID)
}

func (c client) GetWebhookDeliveryJob(ctx context.Context, deliveryID, workspaceID string) (coreapi.WebhookDeliveryJob, error) {
	return c.jobSource().GetWebhookDeliveryJob(ctx, deliveryID, workspaceID)
}

func (c client) GetTestSendContent(ctx context.Context, workspaceID, campaignID, stepID string) (coreapi.TestSendContent, error) {
	return c.jobSource().GetTestSendContent(ctx, workspaceID, campaignID, stepID)
}

func (c client) ResolveSenderTransport(ctx context.Context, workspaceID, mailboxID string) (coreapi.SenderTransport, error) {
	return c.jobSource().ResolveSenderTransport(ctx, workspaceID, mailboxID)
}

func (c client) FindSendByMessageID(ctx context.Context, workspaceID, messageID string) (coreapi.SendRef, error) {
	return c.jobSource().FindSendByMessageID(ctx, workspaceID, messageID)
}
