// Package oauthprovider is the DECOUPLED OAuth 2.1 authorization server (Inroad
// acting as an OAuth PROVIDER): dynamic client registration, the authorization
// endpoint, and the consent handoff (P6a). It is fully self-contained — no product
// domain imports it — and it never re-implements login: it resolves the current
// resource owner through the narrow ResourceOwner seam it defines, backed at the
// composition root by the P1 identity session.
//
// This is unrelated to the mailbox-connect OAuth flow (Inroad as an OAuth CLIENT to
// Google/Microsoft), which mounts under /oauth; this provider mounts under /oauth2.
package oauthprovider

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// CreateClientParams is the persistence input for a registered client, in clean Go
// types so the service never handles pgtype values.
type CreateClientParams struct {
	ClientID                string
	ClientSecretHash        []byte // nil for a public PKCE client
	ClientName              string
	RedirectURIs            []string
	GrantTypes              []string
	ResponseTypes           []string
	Scopes                  []string
	ClientType              string
	TokenEndpointAuthMethod string
	CreatedBy               *uuid.UUID // nil for an anonymous registration
	WorkspaceID             *uuid.UUID // nil for an anonymous registration
}

// CreateAuthRequestParams is the persistence input for a pending consent request.
type CreateAuthRequestParams struct {
	ConsentID           string
	ClientID            string
	RedirectURI         string
	Scopes              []string
	State               *string
	CodeChallenge       string
	CodeChallengeMethod string
	UserID              uuid.UUID
	WorkspaceID         uuid.UUID
	ExpiresAt           time.Time
}

// CreateAuthCodeParams is the persistence input for a single-use authorization code.
type CreateAuthCodeParams struct {
	CodeHash            []byte
	ClientID            string
	RedirectURI         string
	CodeChallenge       string
	CodeChallengeMethod string
	Scopes              []string
	UserID              uuid.UUID
	WorkspaceID         uuid.UUID
	ExpiresAt           time.Time
}

// ApproveParams drives the atomic consent-approval transaction: consume the pending
// request, upsert the remembered consent, and insert the issued code — all or
// nothing.
type ApproveParams struct {
	ConsentID   string
	UserID      uuid.UUID
	ClientID    string
	Scopes      []string
	WorkspaceID uuid.UUID
	Code        CreateAuthCodeParams
}

// Store is the persistence seam the service depends on (dependency inversion):
// exactly the methods it uses, so unit tests inject an in-memory fake with no DB.
// *PgStore satisfies it.
type Store interface {
	// CreateClient persists a registered client and returns the stored row.
	CreateClient(ctx context.Context, p CreateClientParams) (gen.OauthClient, error)
	// GetClient resolves a client by its public client_id (pgx.ErrNoRows if unknown).
	GetClient(ctx context.Context, clientID string) (gen.OauthClient, error)
	// ListClientsByWorkspace returns a workspace's clients WITHOUT any secret hash.
	ListClientsByWorkspace(ctx context.Context, ws uuid.UUID) ([]gen.ListOauthClientsByWorkspaceRow, error)
	// RevokeClient marks (ws, clientID) revoked, tenant-pinned and idempotent;
	// returns rows affected (0 = unknown or foreign workspace).
	RevokeClient(ctx context.Context, ws uuid.UUID, clientID string) (int64, error)

	// CreateAuthRequest persists a fully-validated pending consent request.
	CreateAuthRequest(ctx context.Context, p CreateAuthRequestParams) error
	// GetAuthRequest loads a pending request by consent_id (pgx.ErrNoRows if unknown).
	GetAuthRequest(ctx context.Context, consentID string) (gen.OauthAuthorizationRequest, error)
	// ConsumeAuthRequest single-use-consumes a pending request pinned to its owner
	// and TTL; returns rows affected (0 = unknown/consumed/expired/not this user's).
	ConsumeAuthRequest(ctx context.Context, consentID string, userID uuid.UUID) (int64, error)

	// GetConsent returns the remembered consent for (user, client) (pgx.ErrNoRows if
	// none).
	GetConsent(ctx context.Context, userID uuid.UUID, clientID string) (gen.OauthConsent, error)

	// CreateAuthCode persists a single-use authorization code (prior-consent-skip
	// path, which issues a code without a consent handoff).
	CreateAuthCode(ctx context.Context, p CreateAuthCodeParams) error

	// Approve runs the consent-approval transaction atomically (see ApproveParams).
	// ErrRequestNotClaimable is returned when the pending request could not be
	// consumed (unknown/expired/already used/not this user's).
	Approve(ctx context.Context, p ApproveParams) error
}

// PgStore is the sqlc-backed persistence for the oauthprovider domain. It holds the
// pool directly for the one multi-statement operation that must be atomic (consent
// approval), matching the identity/emailotp/twofa store shape.
type PgStore struct {
	pool *pgxpool.Pool
	q    *gen.Queries
}

// NewPgStore builds a PgStore over the given pool.
func NewPgStore(pool *pgxpool.Pool) *PgStore {
	return &PgStore{pool: pool, q: gen.New(pool)}
}

func (s *PgStore) CreateClient(ctx context.Context, p CreateClientParams) (gen.OauthClient, error) {
	return s.q.CreateOauthClient(ctx, gen.CreateOauthClientParams{
		ClientID:                p.ClientID,
		ClientSecretHash:        p.ClientSecretHash,
		ClientName:              p.ClientName,
		RedirectUris:            p.RedirectURIs,
		GrantTypes:              p.GrantTypes,
		ResponseTypes:           p.ResponseTypes,
		Scopes:                  p.Scopes,
		ClientType:              p.ClientType,
		TokenEndpointAuthMethod: p.TokenEndpointAuthMethod,
		CreatedByUserID:         pgUUIDPtr(p.CreatedBy),
		WorkspaceID:             pgUUIDPtr(p.WorkspaceID),
	})
}

func (s *PgStore) GetClient(ctx context.Context, clientID string) (gen.OauthClient, error) {
	return s.q.GetOauthClient(ctx, clientID)
}

func (s *PgStore) ListClientsByWorkspace(ctx context.Context, ws uuid.UUID) ([]gen.ListOauthClientsByWorkspaceRow, error) {
	return s.q.ListOauthClientsByWorkspace(ctx, pgUUID(ws))
}

func (s *PgStore) RevokeClient(ctx context.Context, ws uuid.UUID, clientID string) (int64, error) {
	return s.q.RevokeOauthClient(ctx, gen.RevokeOauthClientParams{ClientID: clientID, WorkspaceID: pgUUID(ws)})
}

func (s *PgStore) CreateAuthRequest(ctx context.Context, p CreateAuthRequestParams) error {
	return s.q.CreateOauthAuthRequest(ctx, gen.CreateOauthAuthRequestParams{
		ConsentID:           p.ConsentID,
		ClientID:            p.ClientID,
		RedirectUri:         p.RedirectURI,
		Scopes:              p.Scopes,
		State:               p.State,
		CodeChallenge:       p.CodeChallenge,
		CodeChallengeMethod: p.CodeChallengeMethod,
		UserID:              p.UserID,
		WorkspaceID:         p.WorkspaceID,
		ExpiresAt:           pgTime(p.ExpiresAt),
	})
}

func (s *PgStore) GetAuthRequest(ctx context.Context, consentID string) (gen.OauthAuthorizationRequest, error) {
	return s.q.GetOauthAuthRequest(ctx, consentID)
}

func (s *PgStore) ConsumeAuthRequest(ctx context.Context, consentID string, userID uuid.UUID) (int64, error) {
	return s.q.ConsumeOauthAuthRequest(ctx, gen.ConsumeOauthAuthRequestParams{ConsentID: consentID, UserID: userID})
}

func (s *PgStore) GetConsent(ctx context.Context, userID uuid.UUID, clientID string) (gen.OauthConsent, error) {
	return s.q.GetOauthConsent(ctx, gen.GetOauthConsentParams{UserID: userID, ClientID: clientID})
}

func (s *PgStore) CreateAuthCode(ctx context.Context, p CreateAuthCodeParams) error {
	return s.q.CreateOauthAuthCode(ctx, codeParams(p))
}

// Approve consumes the pending request, upserts the remembered consent, and inserts
// the issued code in ONE transaction: either the user has a fresh single-use code and
// a recorded consent, or nothing changed. Consuming FIRST (and requiring exactly one
// affected row) makes approval strictly single-use — a double-submitted approve loses
// the claim and returns ErrRequestNotClaimable rather than minting a second code.
func (s *PgStore) Approve(ctx context.Context, p ApproveParams) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op once committed
	qtx := s.q.WithTx(tx)

	n, err := qtx.ConsumeOauthAuthRequest(ctx, gen.ConsumeOauthAuthRequestParams{
		ConsentID: p.ConsentID, UserID: p.UserID,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrRequestNotClaimable
	}
	if err := qtx.UpsertOauthConsent(ctx, gen.UpsertOauthConsentParams{
		UserID:      p.UserID,
		ClientID:    p.ClientID,
		Scopes:      p.Scopes,
		WorkspaceID: p.WorkspaceID,
	}); err != nil {
		return err
	}
	if err := qtx.CreateOauthAuthCode(ctx, codeParams(p.Code)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// codeParams maps the clean-typed code input to the sqlc params (shared by the
// prior-consent-skip path and the approval transaction).
func codeParams(p CreateAuthCodeParams) gen.CreateOauthAuthCodeParams {
	return gen.CreateOauthAuthCodeParams{
		CodeHash:            p.CodeHash,
		ClientID:            p.ClientID,
		RedirectUri:         p.RedirectURI,
		CodeChallenge:       p.CodeChallenge,
		CodeChallengeMethod: p.CodeChallengeMethod,
		Scopes:              p.Scopes,
		UserID:              p.UserID,
		WorkspaceID:         p.WorkspaceID,
		ExpiresAt:           pgTime(p.ExpiresAt),
	}
}

// Compile-time proof the pg store satisfies the domain seam.
var _ Store = (*PgStore)(nil)
