// Package tracking serves the PUBLIC open-pixel and click-redirect
// endpoints a recipient's mail client follows unauthenticated. It records
// tracking_events for the send/campaign metrics computed in the campaign
// domain (Task 6).
package tracking

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// Store is the repository interface this domain depends on. It is defined
// here (by the consumer), not by the persistence layer, so the service can
// be unit-tested against a fake without a database.
type Store interface {
	// RecordEvent inserts a tracking_events row. workspaceID and campaignID
	// must already be resolved server-side (via ResolveSend) -- callers
	// must never pass values sourced from the token or the request, since
	// send_id has no FK and is the only integrity boundary here.
	RecordEvent(ctx context.Context, workspaceID, campaignID, sendID uuid.UUID, kind, url, userAgent string) error
	// ResolveSend maps a sendID to the workspace/campaign that own it,
	// looked up from the sends row itself. ok is false if no such send
	// exists (a forged or stale sendID), in which case callers must record
	// nothing.
	ResolveSend(ctx context.Context, sendID uuid.UUID) (workspaceID, campaignID uuid.UUID, ok bool)
}

// PgStore implements Store by wrapping sqlc-generated queries.
type PgStore struct{ q *gen.Queries }

// NewPgStore builds a PgStore backed by the given connection pool.
func NewPgStore(pool *pgxpool.Pool) *PgStore { return &PgStore{q: gen.New(pool)} }

// RecordEvent inserts the tracking event.
func (s *PgStore) RecordEvent(ctx context.Context, workspaceID, campaignID, sendID uuid.UUID, kind, url, userAgent string) error {
	return s.q.InsertTrackingEvent(ctx, gen.InsertTrackingEventParams{
		WorkspaceID: workspaceID,
		CampaignID:  campaignID,
		SendID:      sendID,
		Kind:        gen.TrackingEventKind(kind),
		Url:         url,
		UserAgent:   userAgent,
	})
}

// ResolveSend looks up the send's owning workspace/campaign. Any error
// (including "no rows" for an unknown send) is treated as not-found -- the
// caller doesn't need to distinguish a bad id from a transient DB error,
// since either way there is nothing safe to record.
func (s *PgStore) ResolveSend(ctx context.Context, sendID uuid.UUID) (uuid.UUID, uuid.UUID, bool) {
	row, err := s.q.GetCampaignIDForSend(ctx, sendID)
	if err != nil {
		return uuid.Nil, uuid.Nil, false
	}
	return row.WorkspaceID, row.CampaignID, true
}
