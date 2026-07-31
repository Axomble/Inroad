package passkey

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// Store is the persistence seam the service depends on (dependency inversion):
// exactly the methods it uses, so unit tests inject an in-memory fake with no DB.
// *PgStore satisfies it.
type Store interface {
	// CreateCredential persists a freshly-registered credential and returns the
	// stored row (with its generated id). A duplicate credential_id surfaces the
	// underlying unique-violation error for the service to map.
	CreateCredential(ctx context.Context, arg gen.CreateWebAuthnCredentialParams) (gen.WebauthnCredential, error)
	// GetCredentialByCredentialID resolves a credential (and thus its owning user)
	// from the raw credential id presented in a login assertion.
	GetCredentialByCredentialID(ctx context.Context, credentialID []byte) (gen.WebauthnCredential, error)
	// ListCredentialsByUser lists a user's credentials for the manage surface.
	ListCredentialsByUser(ctx context.Context, userID uuid.UUID) ([]gen.WebauthnCredential, error)
	// TouchSignCount advances a credential's stored signature counter (never
	// backwards — the query guards signCount >= stored) and stamps last_used_at,
	// user-pinned. Returns rows affected.
	TouchSignCount(ctx context.Context, id, userID uuid.UUID, signCount int64) (int64, error)
	// DeleteCredential removes a credential own-only; returns rows affected (0 for a
	// foreign or missing id).
	DeleteCredential(ctx context.Context, id, userID uuid.UUID) (int64, error)
	// CreateChallenge persists a server-side ceremony challenge (session key hash,
	// serialized SessionData, kind, TTL). userID is nil for a discoverable login.
	CreateChallenge(ctx context.Context, sessionKey []byte, userID *uuid.UUID, sessionData []byte, kind string, expiresAt time.Time) (uuid.UUID, error)
	// ConsumeChallenge atomically claims a live (unconsumed, unexpired) challenge by
	// session-key hash and returns its stored state. pgx.ErrNoRows means the
	// ceremony is dead (unknown/consumed/expired).
	ConsumeChallenge(ctx context.Context, sessionKey []byte) (gen.WebauthnChallenge, error)
	// GetUserEmail returns a user's email to label the credential in the
	// authenticator UI at registration.
	GetUserEmail(ctx context.Context, userID uuid.UUID) (string, error)
}

// PgStore is the sqlc-backed persistence for the passkey domain.
type PgStore struct {
	q *gen.Queries
}

// NewPgStore builds a PgStore over the given pool.
func NewPgStore(pool *pgxpool.Pool) *PgStore {
	return &PgStore{q: gen.New(pool)}
}

// CreateCredential inserts a registered credential and returns the stored row.
func (s *PgStore) CreateCredential(ctx context.Context, arg gen.CreateWebAuthnCredentialParams) (gen.WebauthnCredential, error) {
	return s.q.CreateWebAuthnCredential(ctx, arg)
}

// GetCredentialByCredentialID looks up a credential by its raw id.
func (s *PgStore) GetCredentialByCredentialID(ctx context.Context, credentialID []byte) (gen.WebauthnCredential, error) {
	return s.q.GetWebAuthnCredentialByCredentialID(ctx, credentialID)
}

// ListCredentialsByUser lists a user's credentials, oldest first.
func (s *PgStore) ListCredentialsByUser(ctx context.Context, userID uuid.UUID) ([]gen.WebauthnCredential, error) {
	return s.q.ListWebAuthnCredentialsByUser(ctx, userID)
}

// TouchSignCount advances the credential's signature counter + last_used_at.
func (s *PgStore) TouchSignCount(ctx context.Context, id, userID uuid.UUID, signCount int64) (int64, error) {
	return s.q.TouchWebAuthnCredentialSignCount(ctx, gen.TouchWebAuthnCredentialSignCountParams{
		ID: id, UserID: userID, SignCount: signCount,
	})
}

// DeleteCredential removes a credential own-only.
func (s *PgStore) DeleteCredential(ctx context.Context, id, userID uuid.UUID) (int64, error) {
	return s.q.DeleteWebAuthnCredential(ctx, gen.DeleteWebAuthnCredentialParams{ID: id, UserID: userID})
}

// CreateChallenge persists a ceremony challenge.
func (s *PgStore) CreateChallenge(ctx context.Context, sessionKey []byte, userID *uuid.UUID, sessionData []byte, kind string, expiresAt time.Time) (uuid.UUID, error) {
	return s.q.CreateWebAuthnChallenge(ctx, gen.CreateWebAuthnChallengeParams{
		SessionKey:  sessionKey,
		UserID:      pgxUUIDPtr(userID),
		SessionData: sessionData,
		Kind:        kind,
		ExpiresAt:   pgxTimestamp(expiresAt),
	})
}

// ConsumeChallenge atomically claims a live challenge and returns its state.
func (s *PgStore) ConsumeChallenge(ctx context.Context, sessionKey []byte) (gen.WebauthnChallenge, error) {
	return s.q.ConsumeWebAuthnChallenge(ctx, sessionKey)
}

// GetUserEmail returns a user's email, reusing the identity domain's query.
func (s *PgStore) GetUserEmail(ctx context.Context, userID uuid.UUID) (string, error) {
	u, err := s.q.GetUserByID(ctx, userID)
	if err != nil {
		return "", err
	}
	return u.Email, nil
}
