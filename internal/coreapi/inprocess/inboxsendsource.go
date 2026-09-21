package inprocess

import (
	"context"

	"github.com/inroad/inroad/internal/coreapi"
)

// InboxSendSource is the MANUAL MAIL protocol: everything internal/worker/inbox
// needs to deliver a reply or a composed email that a HUMAN wrote and pressed
// send on — the reply job read, the claim family for a deferred reply and for a
// deferred compose, and the record of what went out.
//
// It is the seam the coreapi REMOTE transport plugs into
// (internal/coreapi/remote, slice 3b of the plane split), exactly as
// SuppressionSource, JobSource and OutcomeSource are for slices 1, 2 and 3.
//
// Default is localInboxSends — the pool-backed calls every self-hosted
// installation runs, unchanged. A fleet worker replaces the source with an HTTP
// call to the control plane and then none of these twelve methods needs a
// database.
//
// # Why the whole protocol, and not the halves it splits into
//
// Slice 3 moved the step and warmup CLAIM/OUTCOME writes and deliberately left
// this alone, because ClaimPendingInboxReply is a claim-and-READ hybrid — it
// takes the lease and returns the BODY in one call — so moving its outcome half
// without its read half would have reproduced exactly the split that slice
// argued against for the step claim. The four pending-reply methods are one
// protocol; so are the four compose ones; and GetInboxReplyJob/RecordInboxReply
// are the legacy drain's half of the same conversation. Half a claim protocol is
// not a smaller protocol: a claim with no completion is a lease nothing can give
// back.
//
// # What is different about this one
//
// These are emails a person composed and sent. A duplicate sequence step is a
// marketing annoyance; a duplicate manual reply is the operator's own words
// arriving twice in a customer's thread. The double-send bar is therefore
// HIGHER, not equal, and the claim that holds it is the ROW — a status-guarded
// 'scheduled' -> 'sending' UPDATE with a lease, which is also the operator's undo
// handle. See internal/coreapi/remote/inboxsends.go for what a lost response
// costs on each of the twelve.
//
// Signature note: ids are STRINGS, matching the coreapi seam and the three
// worker interfaces that consume these, so each implementation parses them
// itself and a malformed id is an error on both transports rather than an error
// on one and a query on the other.
type InboxSendSource interface {
	// The legacy immediate-reply drain (worker/inbox.ReplyCore). Deleted with
	// ReplySendHandler in the release after this one — see its doc.
	GetInboxReplyJob(ctx context.Context, threadID, workspaceID string) (coreapi.InboxReplyJob, error)
	ClaimInboxReply(ctx context.Context, workspaceID, taskID string) (bool, error)
	ReleaseInboxReply(ctx context.Context, workspaceID, taskID string) error

	// RecordInboxReply is shared by the drain and the deferred path: both record
	// the outbound message onto the thread after the provider ACK.
	RecordInboxReply(ctx context.Context, in coreapi.RecordInboxReplyInput) error

	// Deferred manual replies (worker/inbox.PendingReplyCore).
	ClaimPendingInboxReply(ctx context.Context, workspaceID, pendingID string) (coreapi.PendingInboxReply, error)
	MarkPendingInboxReplySent(ctx context.Context, workspaceID, pendingID, messageID string) error
	ReleasePendingInboxReply(ctx context.Context, workspaceID, pendingID, reason string) error
	FailPendingInboxReply(ctx context.Context, workspaceID, pendingID, reason string) error

	// Deferred composed emails (worker/inbox.ComposeCore).
	ClaimPendingInboxCompose(ctx context.Context, workspaceID, pendingID string) (coreapi.PendingInboxCompose, error)
	MarkPendingInboxComposeSent(ctx context.Context, workspaceID, pendingID, messageID string) error
	ReleasePendingInboxCompose(ctx context.Context, workspaceID, pendingID, reason string) error
	FailPendingInboxCompose(ctx context.Context, workspaceID, pendingID, reason string) error
}

// localInboxSends is the in-process source: the SAME calls that shipped before
// this seam existed, reached through the client that holds the pool.
//
// It holds the client by VALUE and is built fresh in inboxSendSource() rather
// than stored at construction, for the reason localJobs and localOutcomes do: a
// stored copy would be a copy taken at one instant, and every reader would have
// to satisfy themselves it was taken after the options had been applied.
type localInboxSends struct{ c client }

var _ InboxSendSource = localInboxSends{}

func (l localInboxSends) GetInboxReplyJob(ctx context.Context, threadID, workspaceID string) (coreapi.InboxReplyJob, error) {
	return l.c.localGetInboxReplyJob(ctx, threadID, workspaceID)
}

func (l localInboxSends) RecordInboxReply(ctx context.Context, in coreapi.RecordInboxReplyInput) error {
	return l.c.localRecordInboxReply(ctx, in)
}

func (l localInboxSends) ClaimInboxReply(ctx context.Context, workspaceID, taskID string) (bool, error) {
	return l.c.localClaimInboxReply(ctx, workspaceID, taskID)
}

func (l localInboxSends) ReleaseInboxReply(ctx context.Context, workspaceID, taskID string) error {
	return l.c.localReleaseInboxReply(ctx, workspaceID, taskID)
}

func (l localInboxSends) ClaimPendingInboxReply(ctx context.Context, workspaceID, pendingID string) (coreapi.PendingInboxReply, error) {
	return l.c.localClaimPendingInboxReply(ctx, workspaceID, pendingID)
}

func (l localInboxSends) MarkPendingInboxReplySent(ctx context.Context, workspaceID, pendingID, messageID string) error {
	return l.c.localMarkPendingInboxReplySent(ctx, workspaceID, pendingID, messageID)
}

func (l localInboxSends) ReleasePendingInboxReply(ctx context.Context, workspaceID, pendingID, reason string) error {
	return l.c.localReleasePendingInboxReply(ctx, workspaceID, pendingID, reason)
}

func (l localInboxSends) FailPendingInboxReply(ctx context.Context, workspaceID, pendingID, reason string) error {
	return l.c.localFailPendingInboxReply(ctx, workspaceID, pendingID, reason)
}

func (l localInboxSends) ClaimPendingInboxCompose(ctx context.Context, workspaceID, pendingID string) (coreapi.PendingInboxCompose, error) {
	return l.c.localClaimPendingInboxCompose(ctx, workspaceID, pendingID)
}

func (l localInboxSends) MarkPendingInboxComposeSent(ctx context.Context, workspaceID, pendingID, messageID string) error {
	return l.c.localMarkPendingInboxComposeSent(ctx, workspaceID, pendingID, messageID)
}

func (l localInboxSends) ReleasePendingInboxCompose(ctx context.Context, workspaceID, pendingID, reason string) error {
	return l.c.localReleasePendingInboxCompose(ctx, workspaceID, pendingID, reason)
}

func (l localInboxSends) FailPendingInboxCompose(ctx context.Context, workspaceID, pendingID, reason string) error {
	return l.c.localFailPendingInboxCompose(ctx, workspaceID, pendingID, reason)
}

// inboxSendSource resolves where this client sends manual mail from. A nil
// c.inboxSends means "here, through the pool", which is the self-host path and
// what a worker that sets no new variable gets. Same nil-means-local shape as
// jobSource and outcomeSource, and for the same reason: these calls need the
// whole client — the composed inbox.Service, the idempotency store — so a default
// installed in New would have to capture a copy of the client mid-construction.
func (c client) inboxSendSource() InboxSendSource {
	if c.inboxSends != nil {
		return c.inboxSends
	}
	return localInboxSends{c: c}
}

// The exported methods, each one line. They exist so every consumer of this
// protocol goes through the ONE field above — swapping it moves them all at once
// and there is no second path to forget, which is the property that made slice
// 1's SuppressionSource worth having.

func (c client) GetInboxReplyJob(ctx context.Context, threadID, workspaceID string) (coreapi.InboxReplyJob, error) {
	return c.inboxSendSource().GetInboxReplyJob(ctx, threadID, workspaceID)
}

func (c client) RecordInboxReply(ctx context.Context, in coreapi.RecordInboxReplyInput) error {
	return c.inboxSendSource().RecordInboxReply(ctx, in)
}

func (c client) ClaimInboxReply(ctx context.Context, workspaceID, taskID string) (bool, error) {
	return c.inboxSendSource().ClaimInboxReply(ctx, workspaceID, taskID)
}

func (c client) ReleaseInboxReply(ctx context.Context, workspaceID, taskID string) error {
	return c.inboxSendSource().ReleaseInboxReply(ctx, workspaceID, taskID)
}

func (c client) ClaimPendingInboxReply(ctx context.Context, workspaceID, pendingID string) (coreapi.PendingInboxReply, error) {
	return c.inboxSendSource().ClaimPendingInboxReply(ctx, workspaceID, pendingID)
}

func (c client) MarkPendingInboxReplySent(ctx context.Context, workspaceID, pendingID, messageID string) error {
	return c.inboxSendSource().MarkPendingInboxReplySent(ctx, workspaceID, pendingID, messageID)
}

func (c client) ReleasePendingInboxReply(ctx context.Context, workspaceID, pendingID, reason string) error {
	return c.inboxSendSource().ReleasePendingInboxReply(ctx, workspaceID, pendingID, reason)
}

func (c client) FailPendingInboxReply(ctx context.Context, workspaceID, pendingID, reason string) error {
	return c.inboxSendSource().FailPendingInboxReply(ctx, workspaceID, pendingID, reason)
}

func (c client) ClaimPendingInboxCompose(ctx context.Context, workspaceID, pendingID string) (coreapi.PendingInboxCompose, error) {
	return c.inboxSendSource().ClaimPendingInboxCompose(ctx, workspaceID, pendingID)
}

func (c client) MarkPendingInboxComposeSent(ctx context.Context, workspaceID, pendingID, messageID string) error {
	return c.inboxSendSource().MarkPendingInboxComposeSent(ctx, workspaceID, pendingID, messageID)
}

func (c client) ReleasePendingInboxCompose(ctx context.Context, workspaceID, pendingID, reason string) error {
	return c.inboxSendSource().ReleasePendingInboxCompose(ctx, workspaceID, pendingID, reason)
}

func (c client) FailPendingInboxCompose(ctx context.Context, workspaceID, pendingID, reason string) error {
	return c.inboxSendSource().FailPendingInboxCompose(ctx, workspaceID, pendingID, reason)
}
