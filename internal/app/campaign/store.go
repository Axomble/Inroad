package campaign

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

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
	Stats(ctx context.Context, ws, id uuid.UUID) (map[string]int64, error)
	// LaunchTx materializes one `sends` row per (campaign, list member) pair
	// AND transitions the campaign to running, atomically. Returns the ids of
	// newly created rows. Either both writes commit or neither does — a
	// partial launch cannot leak a mixed status.
	LaunchTx(ctx context.Context, ws, campaignID uuid.UUID) ([]uuid.UUID, error)
}

// Checker validates cross-domain references belong to the workspace.
// Implemented in cmd/inroad wiring by a small adapter over the mailbox and
// list stores (Task 9).
type Checker interface {
	MailboxActive(ctx context.Context, ws, mailboxID uuid.UUID) (bool, error)
	ListExists(ctx context.Context, ws, listID uuid.UUID) (bool, error)
}

// PgStore implements Store by wrapping sqlc-generated queries.
type PgStore struct {
	pool *pgxpool.Pool
	q    *gen.Queries
}

// NewPgStore constructs a PgStore backed by the given connection pool. The
// pool is used for LaunchTx's transaction; every other method flows through
// the pool-bound *gen.Queries.
func NewPgStore(pool *pgxpool.Pool) *PgStore {
	return &PgStore{pool: pool, q: gen.New(pool)}
}

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
func (s *PgStore) Stats(ctx context.Context, ws, id uuid.UUID) (map[string]int64, error) {
	rows, err := s.q.CountSendsByStatus(ctx, gen.CountSendsByStatusParams{CampaignID: id, WorkspaceID: ws})
	if err != nil {
		return nil, err
	}
	out := map[string]int64{}
	for _, r := range rows {
		out[r.Status] = r.N
	}
	return out, nil
}

// LaunchTx enqueues sends and flips status to running in a single database
// transaction. If either write fails the transaction is rolled back, leaving
// the campaign as draft with no sends created.
func (s *PgStore) LaunchTx(ctx context.Context, ws, campaignID uuid.UUID) ([]uuid.UUID, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	qtx := s.q.WithTx(tx)
	ids, err := qtx.EnqueueSends(ctx, gen.EnqueueSendsParams{ID: campaignID, WorkspaceID: ws})
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		// Empty target list: don't flip status, don't commit. The service layer
		// maps this to ErrEmptyList.
		return nil, nil
	}
	if err := qtx.SetCampaignStatus(ctx, gen.SetCampaignStatusParams{
		ID:          campaignID,
		WorkspaceID: ws,
		Status:      string(StatusRunning),
		LaunchedAt:  pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return ids, nil
}
