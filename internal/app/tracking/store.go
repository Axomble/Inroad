// Package tracking serves the PUBLIC open-pixel and click-redirect
// endpoints a recipient's mail client follows unauthenticated. It records
// tracking_events for the send/campaign metrics computed in the campaign
// domain (Task 6).
//
// Every recorded event carries a HUMAN/MACHINE classification from
// platform/botfilter, so a prefetch by Apple MPP, the Gmail image proxy or a
// corporate link scanner is STORED but excluded from the headline open/click
// rate. Machine events are never dropped: reporting has to be able to say "N
// opens, M of them machine" rather than silently present a truncated count.
package tracking

import (
	"context"
	"fmt"
	"net/netip"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/platform/botfilter"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// Event is one tracking hit to record, already classified. It is the domain's
// own write model rather than a pile of positional arguments: the parameter
// list had reached six before the classification added three more, and a
// caller swapping two adjacent strings would still have compiled.
type Event struct {
	WorkspaceID, CampaignID, SendID uuid.UUID
	Kind                            botfilter.Kind
	// URL is the click destination; empty for an open.
	URL string
	// UserAgent is the raw request header, stored as observed.
	UserAgent string
	// Verdict and Reason are botfilter's classification of this hit.
	Verdict botfilter.Verdict
	Reason  botfilter.Reason
	// ClientIP is the resolved source address. An invalid Addr stores NULL —
	// "unknown", never a placeholder the burst rule would then group on.
	ClientIP netip.Addr
}

// Store is the repository interface this domain depends on. It is defined
// here (by the consumer), not by the persistence layer, so the service can
// be unit-tested against a fake without a database.
type Store interface {
	// RecordEvent inserts a tracking_events row. WorkspaceID and CampaignID
	// must already be resolved server-side (via ResolveSend) -- callers
	// must never pass values sourced from the token or the request, since
	// send_id has no FK and is the only integrity boundary here.
	RecordEvent(ctx context.Context, ev Event) error
	// ResolveSend maps a sendID to the workspace/campaign that own it and the
	// time it was sent, looked up from the sends row itself. ok is false if no
	// such send exists (a forged or stale sendID), in which case callers must
	// record nothing. sentAt is the zero Time when the send has not been sent
	// yet, which the classifier reads as "unknown" rather than guessing.
	ResolveSend(ctx context.Context, sendID uuid.UUID) (s Send, ok bool)
	// PriorEvents returns the already-recorded history of sendID that the
	// classifier's ordering and volume rules need. subnet is the address block
	// of the current hit, or the zero Prefix to skip the burst count when no
	// client IP could be determined.
	//
	// An error is returned rather than swallowed, but the CALLER decides what
	// to do with it -- on the tracking hot path a degraded classification is
	// better than a failed pixel.
	PriorEvents(ctx context.Context, sendID uuid.UUID, subnet netip.Prefix, since time.Time) (botfilter.Prior, error)
}

// Send is what ResolveSend recovers about a send: its tenant, its campaign,
// and when it went out (for the prefetch-window rule).
type Send struct {
	WorkspaceID, CampaignID uuid.UUID
	SentAt                  time.Time
}

// PgStore implements Store by wrapping sqlc-generated queries.
type PgStore struct{ q *gen.Queries }

// NewPgStore builds a PgStore backed by the given connection pool.
func NewPgStore(pool *pgxpool.Pool) *PgStore { return &PgStore{q: gen.New(pool)} }

// RecordEvent inserts the tracking event, machine-classified ones included.
func (s *PgStore) RecordEvent(ctx context.Context, ev Event) error {
	params := gen.InsertTrackingEventParams{
		WorkspaceID:   ev.WorkspaceID,
		CampaignID:    ev.CampaignID,
		SendID:        ev.SendID,
		Kind:          gen.TrackingEventKind(ev.Kind),
		Url:           ev.URL,
		UserAgent:     ev.UserAgent,
		IsMachine:     ev.Verdict == botfilter.Machine,
		MachineReason: string(ev.Reason),
	}
	// A nil pointer is SQL NULL. An invalid Addr must never be written as a
	// zero address: the burst rule would then treat every unknown-IP hit as
	// sharing one subnet and condemn them all together.
	if ev.ClientIP.IsValid() {
		addr := ev.ClientIP
		params.ClientIp = &addr
	}
	if err := s.q.InsertTrackingEvent(ctx, params); err != nil {
		return fmt.Errorf("insert tracking event: %w", err)
	}
	return nil
}

// ResolveSend looks up the send's owning workspace/campaign and send time. Any
// error (including "no rows" for an unknown send) is treated as not-found --
// the caller doesn't need to distinguish a bad id from a transient DB error,
// since either way there is nothing safe to record.
func (s *PgStore) ResolveSend(ctx context.Context, sendID uuid.UUID) (Send, bool) {
	row, err := s.q.GetCampaignIDForSend(ctx, sendID)
	if err != nil {
		return Send{}, false
	}
	out := Send{WorkspaceID: row.WorkspaceID, CampaignID: row.CampaignID}
	if row.SentAt.Valid {
		out.SentAt = row.SentAt.Time
	}
	return out, true
}

// PriorEvents reads the classifier's ordering and volume inputs.
//
// The burst count is a SECOND round trip, so it is skipped entirely when the
// hit has no usable client IP -- there is nothing to count against, and the
// pixel path must not pay for a query whose answer cannot matter.
func (s *PgStore) PriorEvents(ctx context.Context, sendID uuid.UUID, subnet netip.Prefix, since time.Time) (botfilter.Prior, error) {
	row, err := s.q.GetSendTrackingContext(ctx, sendID)
	if err != nil {
		return botfilter.Prior{}, fmt.Errorf("read send tracking context: %w", err)
	}
	prior := botfilter.Prior{HumanOpens: int(row.HumanOpens)}
	if row.LastHumanOpenAt.Valid {
		prior.LastHumanOpenAt = row.LastHumanOpenAt.Time
	}
	if !subnet.IsValid() {
		return prior, nil
	}
	n, err := s.q.CountRecentSendOpensFromSubnet(ctx, gen.CountRecentSendOpensFromSubnetParams{
		SendID:    sendID,
		CreatedAt: pgtype.Timestamptz{Time: since, Valid: true},
		Subnet:    subnet.String(),
	})
	if err != nil {
		return prior, fmt.Errorf("count recent send opens from subnet: %w", err)
	}
	prior.OpensFromSubnet = int(n)
	return prior, nil
}
