// Package idempotency is the app-layer persistence for the generic
// Idempotency-Key replay cache. Its PgStore satisfies
// httpx.IdempotencyStore structurally (platform/* defines the interface at
// the seam; app/* provides the only implementation — platform/* must never
// import app/*, so the dependency points this way, same as every other
// domain in this codebase).
package idempotency

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/httpx"
)

// PgStore is the sqlc-backed persistence for idempotency_keys.
type PgStore struct {
	q *gen.Queries
}

var _ httpx.IdempotencyStore = (*PgStore)(nil)

// NewPgStore builds a PgStore over the given pool.
func NewPgStore(pool *pgxpool.Pool) *PgStore {
	return &PgStore{q: gen.New(pool)}
}

// TryInsert attempts to claim (workspaceID, key) with requestHash, atomically
// reclaiming an EXPIRED conflicting row (see InsertIdempotencyKey's query
// comment) rather than requiring a separate purge first. A conflict against
// a row still inside its window makes the underlying
// ON CONFLICT ... DO UPDATE ... WHERE not fire, so RETURNING yields no row
// (pgx.ErrNoRows), which this maps to inserted=false (a genuine conflict, not
// a failure) rather than propagating the sentinel error to the caller.
func (s *PgStore) TryInsert(ctx context.Context, workspaceID, key string, requestHash []byte) (bool, error) {
	ws, err := uuid.Parse(workspaceID)
	if err != nil {
		return false, err
	}
	_, err = s.q.InsertIdempotencyKey(ctx, gen.InsertIdempotencyKeyParams{
		WorkspaceID: ws,
		Key:         key,
		RequestHash: requestHash,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// Get loads the existing row for (workspaceID, key). found=false means no row
// exists (pgx.ErrNoRows is not a failure here either).
func (s *PgStore) Get(ctx context.Context, workspaceID, key string) (httpx.IdempotencyRecord, bool, error) {
	ws, err := uuid.Parse(workspaceID)
	if err != nil {
		return httpx.IdempotencyRecord{}, false, err
	}
	row, err := s.q.GetIdempotencyKey(ctx, gen.GetIdempotencyKeyParams{WorkspaceID: ws, Key: key})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.IdempotencyRecord{}, false, nil
		}
		return httpx.IdempotencyRecord{}, false, err
	}
	rec := httpx.IdempotencyRecord{
		RequestHash:  row.RequestHash,
		StatusCode:   row.StatusCode,
		ResponseBody: row.ResponseBody,
	}
	if row.ContentType != nil {
		rec.ContentType = *row.ContentType
	}
	return rec, true, nil
}

// SetResponse persists the wrapped handler's outcome for (workspaceID, key).
func (s *PgStore) SetResponse(ctx context.Context, workspaceID, key string, statusCode int, body []byte, contentType string) error {
	ws, err := uuid.Parse(workspaceID)
	if err != nil {
		return err
	}
	sc := int32(statusCode)
	var ct *string
	if contentType != "" {
		ct = &contentType
	}
	return s.q.SetIdempotencyResponse(ctx, gen.SetIdempotencyResponseParams{
		StatusCode:   &sc,
		ResponseBody: body,
		ContentType:  ct,
		WorkspaceID:  ws,
		Key:          key,
	})
}

// Delete releases the claim row for (workspaceID, key). Called only after a
// >= 500 response, so a same-key retry re-executes rather than replaying (or
// being permanently blocked behind) a transient server error.
func (s *PgStore) Delete(ctx context.Context, workspaceID, key string) error {
	ws, err := uuid.Parse(workspaceID)
	if err != nil {
		return err
	}
	return s.q.DeleteIdempotencyKey(ctx, gen.DeleteIdempotencyKeyParams{WorkspaceID: ws, Key: key})
}
