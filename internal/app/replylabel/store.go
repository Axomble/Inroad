package replylabel

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// Store is the repository interface this domain depends on (defined by the
// consumer), backed by sqlc. gen.ReplyLabel is the persistence type — there is
// no parallel entity struct, per the repo's "sqlc models are the persistence
// type; the interface boundary is where the decoupling lives" rule.
type Store interface {
	List(ctx context.Context, ws uuid.UUID) ([]gen.ReplyLabel, error)
	Get(ctx context.Context, ws, id uuid.UUID) (gen.ReplyLabel, error)
	// GetByKey resolves a classifier key to its label row. Returns ok=false
	// (not an error) when no label in the workspace claims the key — the
	// documented "degrade to the raw key" case for a deleted custom label.
	GetByKey(ctx context.Context, ws uuid.UUID, key string) (gen.ReplyLabel, bool, error)
	Create(ctx context.Context, ws uuid.UUID, key string, in Input) (gen.ReplyLabel, error)
	Update(ctx context.Context, ws, id uuid.UUID, in Input) (gen.ReplyLabel, error)
	// Reorder assigns positions 0..len(ids)-1 in one transaction. Ids not in
	// the workspace simply match zero rows.
	Reorder(ctx context.Context, ws uuid.UUID, ids []uuid.UUID) error
	// Delete removes a NON-builtin label and reports whether a row went. The
	// builtin guard is in the SQL as well as in the service, so neither alone
	// is load-bearing.
	Delete(ctx context.Context, ws, id uuid.UUID) (bool, error)
}

// PgStore implements Store over the sqlc-generated queries. It holds the pool
// as well as the queries because Reorder rewrites several rows atomically.
type PgStore struct {
	pool *pgxpool.Pool
	q    *gen.Queries
}

func NewPgStore(pool *pgxpool.Pool) *PgStore { return &PgStore{pool: pool, q: gen.New(pool)} }

func (s *PgStore) List(ctx context.Context, ws uuid.UUID) ([]gen.ReplyLabel, error) {
	return s.q.ListReplyLabels(ctx, ws)
}

func (s *PgStore) Get(ctx context.Context, ws, id uuid.UUID) (gen.ReplyLabel, error) {
	return s.q.GetReplyLabel(ctx, gen.GetReplyLabelParams{WorkspaceID: ws, ID: id})
}

func (s *PgStore) GetByKey(ctx context.Context, ws uuid.UUID, key string) (gen.ReplyLabel, bool, error) {
	row, err := s.q.GetReplyLabelByKey(ctx, gen.GetReplyLabelByKeyParams{WorkspaceID: ws, Key: key})
	if err != nil {
		if isNoRows(err) {
			return gen.ReplyLabel{}, false, nil
		}
		return gen.ReplyLabel{}, false, err
	}
	return row, true, nil
}

func (s *PgStore) Create(ctx context.Context, ws uuid.UUID, key string, in Input) (gen.ReplyLabel, error) {
	return s.q.CreateReplyLabel(ctx, gen.CreateReplyLabelParams{
		WorkspaceID: ws, Key: key, Label: in.Label, Color: in.Color,
		StopsEnrollment: in.StopsEnrollment, IsAutomated: in.IsAutomated,
		SuppressesContact: in.SuppressesContact, CapturesDeal: in.CapturesDeal,
		DefersEnrollment: in.DefersEnrollment,
	})
}

func (s *PgStore) Update(ctx context.Context, ws, id uuid.UUID, in Input) (gen.ReplyLabel, error) {
	return s.q.UpdateReplyLabel(ctx, gen.UpdateReplyLabelParams{
		WorkspaceID: ws, ID: id, Label: in.Label, Color: in.Color,
		StopsEnrollment: in.StopsEnrollment, IsAutomated: in.IsAutomated,
		SuppressesContact: in.SuppressesContact, CapturesDeal: in.CapturesDeal,
		DefersEnrollment: in.DefersEnrollment,
	})
}

func (s *PgStore) Reorder(ctx context.Context, ws uuid.UUID, ids []uuid.UUID) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	for i, id := range ids {
		if err := qtx.SetReplyLabelPosition(ctx, gen.SetReplyLabelPositionParams{
			WorkspaceID: ws, ID: id, Position: int32(i),
		}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *PgStore) Delete(ctx context.Context, ws, id uuid.UUID) (bool, error) {
	n, err := s.q.DeleteReplyLabel(ctx, gen.DeleteReplyLabelParams{WorkspaceID: ws, ID: id})
	return n > 0, err
}
