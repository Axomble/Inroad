package oauthprovider

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Token lifetimes. Access tokens are SHORT so a leak is self-limiting (the verifier
// re-checks expiry each request); refresh tokens are long-lived but single-use and
// rotated, with reuse detection revoking the whole family.
const (
	accessTokenTTL  = time.Hour
	refreshTokenTTL = 30 * 24 * time.Hour
)

// bearerTokenType is the RFC 6749 token_type of an issued access token.
const bearerTokenType = "Bearer"

// TokenError is an RFC 6749 §5.2 token-endpoint error: an error code, the HTTP status
// to return it with, and a short, non-sensitive description. The handler renders it as
// the `{"error":..., "error_description":...}` JSON body. It never carries internal
// detail.
type TokenError struct {
	Code   string
	Status int
	Desc   string
}

func (e *TokenError) Error() string { return e.Code + ": " + e.Desc }

func tokenErr(status int, code, desc string) *TokenError {
	return &TokenError{Code: code, Status: status, Desc: desc}
}

// The fixed set of token-endpoint errors this provider returns. invalid_client is 401
// (and carries a Basic WWW-Authenticate challenge, added by the handler); the rest are
// 400. No other product detail is ever leaked to the client.
var (
	errInvalidClient    = tokenErr(http.StatusUnauthorized, "invalid_client", "client authentication failed")
	errInvalidGrant     = tokenErr(http.StatusBadRequest, "invalid_grant", "the grant is invalid, expired, or already used")
	errUnsupportedGrant = tokenErr(http.StatusBadRequest, "unsupported_grant_type", "unsupported grant_type")
	errInvalidScope     = tokenErr(http.StatusBadRequest, "invalid_scope", "requested scope exceeds the granted scope")
	errInvalidRequest   = tokenErr(http.StatusBadRequest, "invalid_request", "the request is missing a required parameter or is malformed")
)

// ClientCredentials are the client-authentication inputs parsed from a token/
// introspection/revocation request: the client_id plus, for a confidential client, its
// secret (from client_secret_basic or client_secret_post). HasSecret distinguishes "no
// secret presented" from an empty one.
type ClientCredentials struct {
	ID        string
	Secret    string
	HasSecret bool
}

// TokenRequest is the parsed /oauth2/token form body plus the authenticated client
// credentials.
type TokenRequest struct {
	GrantType    string
	Code         string
	RedirectURI  string
	CodeVerifier string
	RefreshToken string
	Scope        string
	Client       ClientCredentials
}

// TokenResponse is the RFC 6749 §5.1 successful token response (rendered as snake_case
// JSON by the handler).
type TokenResponse struct {
	AccessToken  string
	TokenType    string
	ExpiresIn    int
	RefreshToken string
	Scope        string
}

// Token runs the OAuth 2.1 token endpoint: authenticate the client, then dispatch on
// the grant type. Only authorization_code and refresh_token are supported (no password,
// implicit, or client_credentials). A *TokenError is returned for every client-facing
// rejection; any other error is an infra fault the handler maps to 500.
func (s *Service) Token(ctx context.Context, req TokenRequest) (TokenResponse, error) {
	client, err := s.authenticateClient(ctx, req.Client)
	if err != nil {
		return TokenResponse{}, err
	}
	switch req.GrantType {
	case grantAuthorizationCode:
		return s.grantAuthorizationCode(ctx, req, client)
	case grantRefreshToken:
		return s.grantRefreshToken(ctx, req, client)
	default:
		return TokenResponse{}, errUnsupportedGrant
	}
}

// authenticateClient resolves and authenticates the requesting client. A public (PKCE)
// client authenticates by presenting a known, non-revoked client_id — its proof of
// possession is PKCE, not a secret. A confidential client MUST present a secret that
// matches the stored digest by constant-time compare. Every failure collapses to
// invalid_client so no path distinguishes "unknown client" from "bad secret".
func (s *Service) authenticateClient(ctx context.Context, cred ClientCredentials) (client, error) {
	if cred.ID == "" {
		return client{}, errInvalidClient
	}
	row, err := s.store.GetClient(ctx, cred.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return client{}, errInvalidClient
		}
		return client{}, err
	}
	if row.RevokedAt.Valid {
		return client{}, errInvalidClient
	}
	if row.ClientType == clientTypeConfidential {
		// A confidential client with no presented secret, or a stored digest that is
		// somehow absent, or a mismatching secret — all reject as invalid_client. The
		// compare is constant-time so a wrong secret is not distinguishable by timing.
		if !cred.HasSecret || len(row.ClientSecretHash) == 0 ||
			subtle.ConstantTimeCompare(hashSecret(cred.Secret), row.ClientSecretHash) != 1 {
			return client{}, errInvalidClient
		}
	}
	return client{id: row.ClientID, grantTypes: row.GrantTypes}, nil
}

// client is the minimal authenticated-client view the grant paths need.
type client struct {
	id         string
	grantTypes []string
}

// grantAuthorizationCode is the authorization_code exchange. The order is
// security-mandated: (1) ATOMICALLY consume the single-use code, (2) verify its
// bindings (client_id + exact redirect_uri), (3) verify the PKCE proof, (4) issue the
// token pair. Consuming first means a replay finds the code already consumed; a failed
// PKCE/binding check after consume simply burns the code (defense against brute force).
func (s *Service) grantAuthorizationCode(ctx context.Context, req TokenRequest, c client) (TokenResponse, error) {
	if req.Code == "" {
		return TokenResponse{}, errInvalidGrant
	}
	code, err := s.store.ConsumeAuthCode(ctx, hashSecret(req.Code))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return TokenResponse{}, errInvalidGrant // unknown / already consumed / expired
		}
		return TokenResponse{}, err
	}
	// (2) Bindings: the code must have been issued to THIS authenticated client and the
	// redirect_uri must EXACTLY match the one bound at authorization.
	if code.ClientID != c.id || req.RedirectURI == "" || req.RedirectURI != code.RedirectUri {
		return TokenResponse{}, errInvalidGrant
	}
	// (3) PKCE proof (S256): BASE64URL(SHA256(code_verifier)) must equal the stored
	// challenge, constant-time. A missing/wrong verifier fails.
	if !verifyPKCE(req.CodeVerifier, code.CodeChallenge) {
		return TokenResponse{}, errInvalidGrant
	}
	// (4) Issue an access token + a NEW refresh-token family bound to the code's owner,
	// workspace, and scopes.
	return s.issueTokens(ctx, c.id, code.UserID, code.WorkspaceID, code.Scopes, uuid.New())
}

// grantRefreshToken rotates a refresh token. It mirrors the P1 session refresh:
// look up by hash; a token that is unknown, expired, revoked, or ALREADY CONSUMED
// (reuse) is rejected — and reuse or a lost rotation race revokes the ENTIRE family.
// On success it single-use-consumes the presented token and issues a new access +
// refresh pair in the SAME family. Scope may only narrow, never widen.
func (s *Service) grantRefreshToken(ctx context.Context, req TokenRequest, c client) (TokenResponse, error) {
	if req.RefreshToken == "" {
		return TokenResponse{}, errInvalidGrant
	}
	row, err := s.store.GetRefreshToken(ctx, hashSecret(req.RefreshToken))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return TokenResponse{}, errInvalidGrant
		}
		return TokenResponse{}, err
	}
	// Ownership: a client may only refresh its own tokens.
	if row.ClientID != c.id {
		return TokenResponse{}, errInvalidGrant
	}
	// Reuse detection: presenting an already-rotated (consumed) or revoked token kills
	// the whole family and rejects — the exact P1 session strategy.
	if row.ConsumedAt.Valid || row.RevokedAt.Valid {
		_, _ = s.store.RevokeRefreshFamily(ctx, row.FamilyID)
		return TokenResponse{}, errInvalidGrant
	}
	// Expired (but never used): reject without a family revoke — it isn't reuse.
	if !s.now().Before(row.ExpiresAt.Time) {
		return TokenResponse{}, errInvalidGrant
	}
	// Scope narrowing: an explicit `scope` must be a subset of the token's scopes; it
	// may never widen. Absent -> carry the original scopes forward.
	scopes := row.Scopes
	if req.Scope != "" {
		requested := parseScope(req.Scope)
		if len(requested) == 0 || !isSubset(requested, row.Scopes) {
			return TokenResponse{}, errInvalidScope
		}
		scopes = requested
	}
	// Guarded rotation consume (TOCTOU): only the winner of a concurrent double-refresh
	// flips the row (n==1); a loser (n==0) means the token was consumed/revoked between
	// our read and here — genuine concurrent reuse, so revoke the family and reject.
	n, err := s.store.ConsumeRefreshToken(ctx, row.TokenHash)
	if err != nil {
		return TokenResponse{}, err
	}
	if n == 0 {
		_, _ = s.store.RevokeRefreshFamily(ctx, row.FamilyID)
		return TokenResponse{}, errInvalidGrant
	}
	return s.issueTokens(ctx, c.id, row.UserID, row.WorkspaceID, scopes, row.FamilyID)
}

// issueTokens mints an opaque access token and a rotating refresh token, persists both
// (hashed) in one transaction bound to (client, user, workspace, scopes, familyID), and
// returns the RFC 6749 token response with the raw values (returned to the client once).
func (s *Service) issueTokens(ctx context.Context, clientID string, userID, wsID uuid.UUID, scopes []string, familyID uuid.UUID) (TokenResponse, error) {
	accessRaw, accessHash, err := newAccessToken()
	if err != nil {
		return TokenResponse{}, err
	}
	refreshRaw, refreshHash, err := newRefreshToken()
	if err != nil {
		return TokenResponse{}, err
	}
	now := s.now()
	if err := s.store.IssueTokenPair(ctx,
		CreateAccessTokenParams{
			TokenHash: accessHash, ClientID: clientID, UserID: userID,
			WorkspaceID: wsID, Scopes: scopes, ExpiresAt: now.Add(accessTokenTTL),
		},
		CreateRefreshTokenParams{
			TokenHash: refreshHash, FamilyID: familyID, ClientID: clientID, UserID: userID,
			WorkspaceID: wsID, Scopes: scopes, ExpiresAt: now.Add(refreshTokenTTL),
		},
	); err != nil {
		return TokenResponse{}, err
	}
	return TokenResponse{
		AccessToken:  accessRaw,
		TokenType:    bearerTokenType,
		ExpiresIn:    int(accessTokenTTL / time.Second),
		RefreshToken: refreshRaw,
		Scope:        strings.Join(scopes, " "),
	}, nil
}

// verifyPKCE performs the S256 PKCE check: BASE64URL-no-pad(SHA256(ASCII(code_verifier)))
// must equal the stored code_challenge, compared in constant time. A missing verifier or
// challenge fails closed. This is the sole proof of possession for a public client, so
// it must be exact.
func verifyPKCE(codeVerifier, storedChallenge string) bool {
	if codeVerifier == "" || storedChallenge == "" {
		return false
	}
	sum := sha256.Sum256([]byte(codeVerifier))
	computed := b64.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(computed), []byte(storedChallenge)) == 1
}

// ---------------------------------------------------------------------------
// Introspection (RFC 7662) + revocation (RFC 7009)
// ---------------------------------------------------------------------------

// IntrospectResult is the RFC 7662 metadata for a token. For an inactive/unknown token
// only Active is set (false) — the handler emits exactly `{"active": false}`, never any
// detail, so the endpoint is not a token-existence oracle.
type IntrospectResult struct {
	Active    bool
	Scope     string
	ClientID  string
	Sub       string
	Exp       int64
	TokenType string
}

// Introspect implements RFC 7662. The requesting client authenticates exactly as at the
// token endpoint. The presented token is looked up as an access token, then a refresh
// token; a live one returns active + metadata, everything else returns inactive. There
// is no oracle beyond active/inactive.
func (s *Service) Introspect(ctx context.Context, token string, client ClientCredentials) (IntrospectResult, error) {
	if _, err := s.authenticateClient(ctx, client); err != nil {
		return IntrospectResult{}, err
	}
	if token == "" {
		return IntrospectResult{Active: false}, nil
	}
	hash := hashSecret(token)

	at, err := s.store.GetAccessToken(ctx, hash)
	switch {
	case err == nil:
		if !at.RevokedAt.Valid && s.now().Before(at.ExpiresAt.Time) {
			return IntrospectResult{
				Active: true, Scope: strings.Join(at.Scopes, " "), ClientID: at.ClientID,
				Sub: at.UserID.String(), Exp: at.ExpiresAt.Time.Unix(), TokenType: bearerTokenType,
			}, nil
		}
		return IntrospectResult{Active: false}, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return IntrospectResult{}, err
	}

	rt, err := s.store.GetRefreshToken(ctx, hash)
	switch {
	case err == nil:
		if !rt.RevokedAt.Valid && !rt.ConsumedAt.Valid && s.now().Before(rt.ExpiresAt.Time) {
			return IntrospectResult{
				Active: true, Scope: strings.Join(rt.Scopes, " "), ClientID: rt.ClientID,
				Sub: rt.UserID.String(), Exp: rt.ExpiresAt.Time.Unix(),
			}, nil
		}
		return IntrospectResult{Active: false}, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return IntrospectResult{}, err
	}

	return IntrospectResult{Active: false}, nil
}

// Revoke implements RFC 7009. The requesting client authenticates as at the token
// endpoint, then may revoke ONLY its own tokens. An access token is revoked directly;
// a refresh token is revoked together with its whole rotation family. An unknown token,
// or one belonging to another client, is a silent no-op — the handler returns 200
// regardless, so there is no token-existence oracle.
func (s *Service) Revoke(ctx context.Context, token string, client ClientCredentials) error {
	authed, err := s.authenticateClient(ctx, client)
	if err != nil {
		return err
	}
	if token == "" {
		return nil
	}
	hash := hashSecret(token)

	// Access token: client-scoped revoke. A hit (n>0) is done.
	n, err := s.store.RevokeAccessToken(ctx, hash, authed.id)
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}

	// Otherwise try a refresh token; revoke its family only if it belongs to this client.
	rt, err := s.store.GetRefreshToken(ctx, hash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // unknown token: no-op, no oracle
		}
		return err
	}
	if rt.ClientID != authed.id {
		return nil // another client's token: no-op, no oracle
	}
	_, err = s.store.RevokeRefreshFamily(ctx, rt.FamilyID)
	return err
}
