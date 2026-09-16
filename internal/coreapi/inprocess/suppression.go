package inprocess

import (
	"context"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// SuppressionSource answers "is this address on this workspace's suppression
// list". It is the seam the coreapi REMOTE transport plugs into
// (internal/coreapi/remote, slice 1 of the plane split): the in-process client
// runs the query itself by default, and a fleet worker replaces the source with
// an HTTP call to the control plane so that one method needs no database.
//
// It is the same shape as credbroker.Opener's role here — one narrow interface
// on the client, a local implementation and a remote one, chosen at the
// composition root — and for the same reason: every consumer of this capability
// goes through the ONE field below, so swapping it moves them all at once and
// there is no second path to forget.
//
// Signature note: this takes workspaceID as a STRING because that is what the
// coreapi seam speaks and what the four execution-plane consumers already pass;
// the uuid parse happens inside each implementation, so a malformed id is an
// error on both transports rather than an error on one and a query on the other.
type SuppressionSource interface {
	IsSuppressed(ctx context.Context, workspaceID, email string) (bool, error)
}

// localSuppression is the in-process source: the SAME
// (workspace_id, lower(email))-indexed lookup GetStepSendJob uses for a real
// send. This is what every self-hosted installation runs, unchanged.
type localSuppression struct{ q *gen.Queries }

var _ SuppressionSource = localSuppression{}

func (l localSuppression) IsSuppressed(ctx context.Context, workspaceID, email string) (bool, error) {
	ws, err := uuid.Parse(workspaceID)
	if err != nil {
		return false, err
	}
	return l.q.IsSuppressed(ctx, gen.IsSuppressedParams{WorkspaceID: ws, Lower: email})
}

// IsSuppressed reports whether `to` is on workspaceID's suppression list.
//
// Four execution-plane handlers consume it, all as a defense-in-depth re-check
// immediately before dialing, because the control-plane check that preceded
// them can race an incoming unsubscribe: worker/testsend (testsend.Core),
// and worker/inbox's manual reply, pending reply and pending compose sends
// (ReplyCore, PendingReplyCore, ComposeCore). Each treats a non-nil error as
// "do not send", which is what makes a remote source safe to fail closed.
//
// GetStepSendJob's own inline gate (the Suppressed flag on a step send) now
// reads through this SAME field rather than going straight at the pool, so the
// sentence above is exact: c.suppression is the one route. Slice 1 left it on
// the pool because routing it while the rest of that job build still read
// Postgres would have blurred what the flag meant; slice 2 moved the whole job
// build, so there is nothing left to blur.
func (c client) IsSuppressed(ctx context.Context, workspaceID, to string) (bool, error) {
	return c.suppression.IsSuppressed(ctx, workspaceID, to)
}
