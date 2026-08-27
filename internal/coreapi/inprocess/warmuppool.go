package inprocess

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/platform/coordinator"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// warmupPool adapts this workspace's participant table to the coordinator's Pool
// port. It is the whole of what makes LocalCoordinator local.
//
// The selection query is unchanged and still owns "who is eligible" — the seam adds
// the tenancy and pairing checks on top rather than re-deciding. A second opinion
// about who may pair with whom is the defect this subsystem keeps producing, so the
// adapter deliberately translates and does not judge.
type warmupPool struct {
	q *gen.Queries
}

// SelectPartner runs the existing recency-spread selection and returns the row as a
// candidate.
//
// WorkspaceID, Lane and IsSentinel are read from the CANDIDATE'S OWN ROW. Copying
// them from the request would make the coordinator's cross-tenant check compare a
// value against itself — a tenancy check that cannot fail, which is worse than none
// because it reads as one.
func (p warmupPool) SelectPartner(ctx context.Context, req coordinator.PairRequest) (coordinator.Candidate, bool, error) {
	ws, err := uuid.Parse(req.Requester.WorkspaceID)
	if err != nil {
		return coordinator.Candidate{}, false, err
	}
	sender, err := uuid.Parse(req.Requester.ID)
	if err != nil {
		return coordinator.Candidate{}, false, err
	}

	row, err := p.q.SelectWarmupPartner(ctx, gen.SelectWarmupPartnerParams{
		WorkspaceID: ws,
		MailboxID:   sender,
		// From the REQUEST, not a field: the pair cap is computed per send from the
		// sender's effective volume and the pool size, so a cached one would be a
		// second, staler answer to a question the caller already resolved.
		MaxPairSends: int32(req.Constraints.MaxPairSendsPerDay),
		CooldownSince: pgtype.Timestamptz{
			Time: req.Constraints.CooldownSince, Valid: true,
		},
	})
	if err != nil {
		// No eligible partner is not an error: a workspace with fewer than two usable
		// participants is an ordinary, working arrangement that simply cannot pair
		// today. The seam turns this into ErrNoPartner, which the caller skips on.
		if errors.Is(err, pgx.ErrNoRows) {
			return coordinator.Candidate{}, false, nil
		}
		return coordinator.Candidate{}, false, err
	}

	return coordinator.Candidate{
		Participant: coordinator.Participant{
			WorkspaceID: row.WorkspaceID.String(),
			ID:          row.MailboxID.String(),
			Lane:        row.Lane,
			IsSentinel:  row.IsSentinel,
		},
		Address: row.Email,
	}, true, nil
}

// coordinator is the pairing authority this client uses.
//
// Constructed per call rather than held on the client: it is a two-field value over
// the same *gen.Queries the client already owns, so there is nothing to cache and
// nothing to keep in step. When a remote coordinator exists this becomes a
// configured field, and that is the ONE place that has to change — which is the
// whole point of routing through the seam while there is only one implementation.
func (c client) coordinator() *coordinator.LocalCoordinator {
	return coordinator.NewLocal(warmupPool{q: c.q})
}
