package oauthprovider

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// verifierStore is the narrow persistence seam the Verifier needs (interface-segregated,
// consumer-defined): just the by-hash access-token lookup. *PgStore satisfies it.
type verifierStore interface {
	GetAccessToken(ctx context.Context, tokenHash []byte) (gen.OauthAccessToken, error)
}

// Verifier authenticates a request bearing an OAuth 2.1 access token. It implements
// auth.Verifier: an `inoa_` Bearer token ENGAGES it (authenticate or definitively
// reject); anything else DEFERS so the api-key/session verifiers still work. It mints a
// KindOAuth principal carrying ONLY the token's granted scopes and an empty role, so an
// OAuth grant can never reach a RequireRole (admin) surface — only RequireScope ones.
type Verifier struct {
	store verifierStore
	// now is overridable in tests for deterministic expiry checks.
	now func() time.Time
}

// VerifyToken returns the same tenant-scoped principal as Verify together with
// the token expiry and OAuth client id. MCP's bearer middleware needs the
// expiry for its own validation, while the agent registry records the client
// id as the delegated actor.
func (v *Verifier) VerifyToken(ctx context.Context, r *http.Request) (auth.Principal, time.Time, string, bool, error) {
	raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || !strings.HasPrefix(raw, accessTokenPrefix) {
		return auth.Principal{}, time.Time{}, "", false, nil
	}
	tok, err := v.store.GetAccessToken(ctx, hashSecret(raw))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return auth.Principal{}, time.Time{}, "", false, auth.ErrUnauthorized
		}
		return auth.Principal{}, time.Time{}, "", false, err
	}
	if tok.RevokedAt.Valid || !v.now().Before(tok.ExpiresAt.Time) {
		return auth.Principal{}, time.Time{}, "", false, auth.ErrUnauthorized
	}
	return auth.Principal{Kind: auth.KindOAuth, UserID: tok.UserID.String(), WorkspaceID: tok.WorkspaceID.String(), Scopes: tok.Scopes}, tok.ExpiresAt.Time, tok.ClientID, true, nil
}

// NewVerifier builds a Verifier over the access-token store seam.
func NewVerifier(store verifierStore) *Verifier {
	return &Verifier{store: store, now: time.Now}
}

// Verify implements auth.Verifier. A non-`inoa_` (or non-Bearer) credential DEFERS. A
// presented `inoa_` token is authenticated by a constant-cost hashed lookup and every
// rejection (unknown, revoked, expired) is fail-closed (ErrUnauthorized); a store outage
// fails loud (500). Revocation and expiry are checked on EVERY request, so a revoke is
// effective immediately.
func (v *Verifier) Verify(ctx context.Context, r *http.Request) (auth.Principal, bool, error) {
	p, _, _, ok, err := v.VerifyToken(ctx, r)
	return p, ok, err
}

// Compile-time proof the verifier satisfies the auth seam.
var _ auth.Verifier = (*Verifier)(nil)
