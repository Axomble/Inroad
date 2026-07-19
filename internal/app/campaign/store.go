package campaign

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// CreateInput carries the fields needed to create a new campaign.
type CreateInput struct {
	Name, Subject, BodyText, BodyHTML string
	MailboxID, ListID                 uuid.UUID
}

// Store is the repository interface this domain depends on. It is defined
// here (by the consumer), not by the persistence layer, so the service can
// be unit-tested against a fake without a database.
type Store interface {
	Create(ctx context.Context, ws uuid.UUID, in CreateInput) (gen.Campaign, error)
	Get(ctx context.Context, ws, id uuid.UUID) (gen.Campaign, error)
	List(ctx context.Context, ws uuid.UUID) ([]gen.Campaign, error)
	Stats(ctx context.Context, id uuid.UUID) (map[string]int64, error)
	// EnqueueSends materializes one `sends` row per (campaign, list member)
	// pair and returns the ids of the newly created rows.
	EnqueueSends(ctx context.Context, ws, campaignID uuid.UUID) ([]uuid.UUID, error)
	// SetStatus transitions a campaign to the given status.
	SetStatus(ctx context.Context, ws, id uuid.UUID, status CampaignStatus) error
}

// Checker validates cross-domain references belong to the workspace.
// Implemented in cmd/inroad wiring by a small adapter over the mailbox and
// list stores (Task 9).
type Checker interface {
	MailboxActive(ctx context.Context, ws, mailboxID uuid.UUID) (bool, error)
	ListExists(ctx context.Context, ws, listID uuid.UUID) (bool, error)
}

// PgStore implements Store by wrapping sqlc-generated queries.
type PgStore struct{ q *gen.Queries }

func NewPgStore(q *gen.Queries) *PgStore { return &PgStore{q: q} }

func (s *PgStore) Create(ctx context.Context, ws uuid.UUID, in CreateInput) (gen.Campaign, error) {
	return s.q.CreateCampaign(ctx, gen.CreateCampaignParams{
		WorkspaceID: ws, Name: in.Name, MailboxID: in.MailboxID, ListID: in.ListID,
		Subject: in.Subject, BodyText: in.BodyText, BodyHtml: in.BodyHTML,
	})
}
func (s *PgStore) Get(ctx context.Context, ws, id uuid.UUID) (gen.Campaign, error) {
	return s.q.GetCampaign(ctx, gen.GetCampaignParams{ID: id, WorkspaceID: ws})
}
func (s *PgStore) List(ctx context.Context, ws uuid.UUID) ([]gen.Campaign, error) {
	return s.q.ListCampaigns(ctx, ws)
}
func (s *PgStore) Stats(ctx context.Context, id uuid.UUID) (map[string]int64, error) {
	rows, err := s.q.CountSendsByStatus(ctx, id)
	if err != nil {
		return nil, err
	}
	out := map[string]int64{}
	for _, r := range rows {
		out[r.Status] = r.N
	}
	return out, nil
}

func (s *PgStore) EnqueueSends(ctx context.Context, ws, campaignID uuid.UUID) ([]uuid.UUID, error) {
	return s.q.EnqueueSends(ctx, gen.EnqueueSendsParams{ID: campaignID, WorkspaceID: ws})
}

func (s *PgStore) SetStatus(ctx context.Context, ws, id uuid.UUID, status CampaignStatus) error {
	return s.q.SetCampaignStatus(ctx, gen.SetCampaignStatusParams{
		ID:          id,
		WorkspaceID: ws,
		Status:      string(status),
		LaunchedAt:  pgtype.Timestamptz{Time: time.Now(), Valid: true},
	})
}
