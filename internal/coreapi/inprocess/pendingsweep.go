package inprocess

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/app/inbox"
	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// ListStrandedPendingInboxSends finds the manual replies and composed emails
// that nothing is going to deliver on their own, across every workspace, for the
// periodic stranded-send sweep (internal/worker/inbox.PendingSweepHandler).
//
// It is a READ that names rows and nothing else. It does not claim, release,
// mark or re-send: the sweep's whole job is to hand ids to a normal send task,
// and the row's own guarded claim then decides — exactly once — whether a send
// happens. A second send path for the same mail is the one thing this subsystem
// cannot afford, so there isn't one.
//
// Cross-tenant by nature, like every other sweep scan (ListDueEnrollments,
// ListActiveMailboxes): it returns workspace_id so each rescue is then
// workspace-pinned one step later, in the send task's own claim.
//
// THE LEASE IS ADDED HERE, not passed in. The caller supplies only a grace, and
// this adds inbox.PendingReplyLeaseSeconds on top, so no configuration a worker
// can express nominates a row whose lease is still live. That is the difference
// between "the sweep respects the lease" as a policy and as a property.
func (c client) ListStrandedPendingInboxSends(ctx context.Context, w coreapi.StrandedPendingWindow) ([]coreapi.StrandedPendingSend, error) {
	overdue := interval(w.OverdueAfter)
	staleClaim := interval(time.Duration(inbox.PendingReplyLeaseSeconds)*time.Second + w.LeaseGrace)

	replies, err := c.q.ListStrandedInboxPendingReplies(ctx, gen.ListStrandedInboxPendingRepliesParams{
		OverdueAfter: overdue, StaleClaimAfter: staleClaim,
	})
	if err != nil {
		return nil, fmt.Errorf("scan stranded pending replies: %w", err)
	}
	composes, err := c.q.ListStrandedInboxPendingComposes(ctx, gen.ListStrandedInboxPendingComposesParams{
		OverdueAfter: overdue, StaleClaimAfter: staleClaim,
	})
	if err != nil {
		return nil, fmt.Errorf("scan stranded pending composes: %w", err)
	}

	out := make([]coreapi.StrandedPendingSend, 0, len(replies)+len(composes))
	for _, r := range replies {
		out = append(out, coreapi.StrandedPendingSend{
			Kind:        coreapi.StrandedPendingKindReply,
			ID:          r.ID.String(),
			WorkspaceID: r.WorkspaceID.String(),
			Status:      r.Status,
		})
	}
	for _, r := range composes {
		out = append(out, coreapi.StrandedPendingSend{
			Kind:        coreapi.StrandedPendingKindCompose,
			ID:          r.ID.String(),
			WorkspaceID: r.WorkspaceID.String(),
			Status:      r.Status,
		})
	}
	return out, nil
}

// interval renders a Go duration as the pgtype.Interval the scan's `::interval`
// casts take, matching FindDuplicatePendingReply's existing conversion.
func interval(d time.Duration) pgtype.Interval {
	return pgtype.Interval{Microseconds: d.Microseconds(), Valid: true}
}
