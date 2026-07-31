package oauthprovider

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// CreateAccessTokenParams is the persistence input for an issued opaque access token,
// in clean Go types so the service never touches pgtype values. FamilyID ties the token
// to its rotation family so a reuse-detection family revoke kills it alongside the
// refresh siblings.
type CreateAccessTokenParams struct {
	TokenHash   []byte
	FamilyID    uuid.UUID
	ClientID    string
	UserID      uuid.UUID
	WorkspaceID uuid.UUID
	Scopes      []string
	ExpiresAt   time.Time
}

// CreateRefreshTokenParams is the persistence input for an issued rotating refresh
// token, carrying the rotation family it belongs to.
type CreateRefreshTokenParams struct {
	TokenHash   []byte
	FamilyID    uuid.UUID
	ClientID    string
	UserID      uuid.UUID
	WorkspaceID uuid.UUID
	Scopes      []string
	ExpiresAt   time.Time
}

func (s *PgStore) ConsumeAuthCode(ctx context.Context, codeHash []byte) (gen.OauthAuthorizationCode, error) {
	return s.q.ConsumeOauthAuthCode(ctx, codeHash)
}

// IssueTokenPair inserts the access + refresh token in one transaction: either both
// land or neither, so an exchange never leaves a half-issued grant.
func (s *PgStore) IssueTokenPair(ctx context.Context, access CreateAccessTokenParams, refresh CreateRefreshTokenParams) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op once committed
	qtx := s.q.WithTx(tx)

	if err := qtx.CreateOauthAccessToken(ctx, gen.CreateOauthAccessTokenParams{
		TokenHash:   access.TokenHash,
		FamilyID:    access.FamilyID,
		ClientID:    access.ClientID,
		UserID:      access.UserID,
		WorkspaceID: access.WorkspaceID,
		Scopes:      access.Scopes,
		ExpiresAt:   pgTime(access.ExpiresAt),
	}); err != nil {
		return err
	}
	if err := qtx.CreateOauthRefreshToken(ctx, gen.CreateOauthRefreshTokenParams{
		TokenHash:   refresh.TokenHash,
		FamilyID:    refresh.FamilyID,
		ClientID:    refresh.ClientID,
		UserID:      refresh.UserID,
		WorkspaceID: refresh.WorkspaceID,
		Scopes:      refresh.Scopes,
		ExpiresAt:   pgTime(refresh.ExpiresAt),
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PgStore) GetAccessToken(ctx context.Context, tokenHash []byte) (gen.OauthAccessToken, error) {
	return s.q.GetOauthAccessTokenByHash(ctx, tokenHash)
}

func (s *PgStore) GetRefreshToken(ctx context.Context, tokenHash []byte) (gen.OauthRefreshToken, error) {
	return s.q.GetOauthRefreshTokenByHash(ctx, tokenHash)
}

func (s *PgStore) ConsumeRefreshToken(ctx context.Context, tokenHash []byte) (int64, error) {
	return s.q.ConsumeOauthRefreshToken(ctx, tokenHash)
}

func (s *PgStore) RevokeRefreshFamily(ctx context.Context, familyID uuid.UUID) (int64, error) {
	return s.q.RevokeOauthRefreshFamily(ctx, familyID)
}

func (s *PgStore) RevokeAccessToken(ctx context.Context, tokenHash []byte, clientID string) (int64, error) {
	return s.q.RevokeOauthAccessToken(ctx, gen.RevokeOauthAccessTokenParams{TokenHash: tokenHash, ClientID: clientID})
}

func (s *PgStore) RevokeAccessFamily(ctx context.Context, familyID uuid.UUID) (int64, error) {
	return s.q.RevokeOauthAccessFamily(ctx, familyID)
}
