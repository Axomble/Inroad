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
	raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || !strings.HasPrefix(raw, accessTokenPrefix) {
		return auth.Principal{}, false, nil // not an OAuth access token: DEFER
	}

	tok, err := v.store.GetAccessToken(ctx, hashSecret(raw))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return auth.Principal{}, false, auth.ErrUnauthorized // unknown token
		}
		return auth.Principal{}, false, err // store failure -> 500 (fail loud)
	}
	if tok.RevokedAt.Valid || !v.now().Before(tok.ExpiresAt.Time) {
		return auth.Principal{}, false, auth.ErrUnauthorized // revoked or expired
	}

	return auth.Principal{
		Kind:        auth.KindOAuth,
		UserID:      tok.UserID.String(),
		WorkspaceID: tok.WorkspaceID.String(),
		Scopes:      tok.Scopes,
		// Role is intentionally empty: a delegated OAuth grant is scope-gated, never
		// role-gated, so it cannot reach RequireRole-guarded (admin) surfaces.
	}, true, nil
}

// Compile-time proof the verifier satisfies the auth seam.
var _ auth.Verifier = (*Verifier)(nil)
