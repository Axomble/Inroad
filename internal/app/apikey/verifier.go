package apikey

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// verifierStore is the narrow persistence seam the verifier needs
// (consumer-defined, interface-segregated): a by-prefix lookup and a best-effort
// last-use touch. *PgStore satisfies it.
type verifierStore interface {
	GetByPrefix(ctx context.Context, prefix string) (gen.ApiKey, error)
	TouchLastUsed(ctx context.Context, id uuid.UUID) error
}

// RateLimiter is the atomic per-key request cap the verifier consults. It is
// consumer-defined here so the domain stays free of any Redis import; the
// composition root injects platform/ratelimit's Redis-backed implementation and
// unit tests inject a fake.
type RateLimiter interface {
	// Allow reports whether one more request under key is within limit for the
	// window. It returns an error (rather than a bool) on infra failure so the
	// caller can FAIL CLOSED — the verifier denies on error, never falls open.
	Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error)
}

// Verifier authenticates a request bearing an api-key token. It implements
// auth.Verifier: an inrd_ token ENGAGES it (authenticate or definitively reject);
// anything else DEFERS so another scheme (the session verifier) can claim it.
type Verifier struct {
	store   verifierStore
	limiter RateLimiter
	ip      clientIPResolver
	// now is overridable in tests for deterministic expiry checks.
	now func() time.Time
}

// NewVerifier builds a Verifier. limiter may be a no-op only in tests that never
// set a per-key rate limit; production always wires a real limiter. trustedProxies
// scopes which peers may set X-Forwarded-For for the IP-allowlist check.
func NewVerifier(store verifierStore, limiter RateLimiter, trustedProxies []string) *Verifier {
	return &Verifier{
		store:   store,
		limiter: limiter,
		ip:      newClientIPResolver(trustedProxies),
		now:     time.Now,
	}
}

// Verify implements auth.Verifier. See the type doc for engage-vs-defer; every
// rejection of a presented api-key token is fail-closed (ErrUnauthorized), a
// store/limiter outage fails loud (500), and an over-limit key is ErrRateLimited
// (429).
func (v *Verifier) Verify(ctx context.Context, r *http.Request) (auth.Principal, bool, error) {
	raw, ok := extractToken(r)
	if !ok || !hasScheme(raw) {
		return auth.Principal{}, false, nil // not an api-key token: DEFER
	}
	prefix, secretHash, ok := parseToken(raw)
	if !ok {
		// Shaped like an api-key token (inrd_ scheme) but malformed: this verifier
		// OWNS it, so reject definitively rather than defer — a later verifier must
		// not be able to paper over a garbled key.
		return auth.Principal{}, false, auth.ErrUnauthorized
	}

	key, err := v.store.GetByPrefix(ctx, prefix)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return auth.Principal{}, false, auth.ErrUnauthorized // unknown key
		}
		return auth.Principal{}, false, err // store failure -> 500 (fail loud)
	}

	// Constant-time secret compare, folded together with the revoked/expired checks
	// into ONE rejection branch so that "found but wrong secret" is not
	// distinguishable — by timing or by which check fired — from "found, correct
	// secret, but revoked/expired". subtle.ConstantTimeCompare avoids a byte-by-byte
	// early-exit an attacker could use to guess the secret.
	secretOK := subtle.ConstantTimeCompare(secretHash, key.SecretHash) == 1
	revoked := key.RevokedAt.Valid
	expired := key.ExpiresAt.Valid && v.now().After(key.ExpiresAt.Time)
	if !secretOK || revoked || expired {
		return auth.Principal{}, false, auth.ErrUnauthorized
	}

	// IP allowlist (fail-closed): when set, the resolved client IP must fall inside
	// a listed CIDR. An indeterminate client IP with an allowlist present is denied.
	if len(key.IpAllowlist) > 0 && !ipAllowed(v.ip.resolve(r), key.IpAllowlist) {
		return auth.Principal{}, false, auth.ErrUnauthorized
	}

	// Rate limit: over the per-minute cap -> 429. A limiter (Redis) error denies the
	// request (fail closed) rather than lifting the cap.
	if key.RateLimitPerMin != nil && *key.RateLimitPerMin > 0 {
		allowed, err := v.limiter.Allow(ctx, key.ID.String(), int(*key.RateLimitPerMin), time.Minute)
		if err != nil {
			return auth.Principal{}, false, err // fail closed -> 500
		}
		if !allowed {
			return auth.Principal{}, false, auth.ErrRateLimited // -> 429
		}
	}

	v.touch(key.ID) // best-effort last-use stamp; never blocks or fails the request

	return auth.Principal{
		Kind:        auth.KindAPIKey,
		WorkspaceID: key.WorkspaceID.String(),
		UserID:      createdByString(key.CreatedByUserID),
		Scopes:      key.Scopes,
		// Role is intentionally empty: a machine principal is scope-gated, never
		// role-gated, so it cannot reach RequireRole-guarded (admin) surfaces.
	}, true, nil
}

// touch stamps last_used_at off the request path. It is fire-and-forget with a
// bounded timeout: a slow or failed update must never delay or fail an otherwise
// authenticated request.
func (v *Verifier) touch(id uuid.UUID) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := v.store.TouchLastUsed(ctx, id); err != nil {
			slog.Warn("apikey: touch last_used failed", "err", err, "key_id", id)
		}
	}()
}

// extractToken reads the presented credential from Authorization: Bearer or,
// failing that, the X-API-Key header. It does NOT judge the value's shape — that
// is hasScheme/parseToken's job — so a Bearer JWT is returned here and then
// deferred on by hasScheme.
func extractToken(r *http.Request) (string, bool) {
	if raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		return raw, true
	}
	if raw := r.Header.Get("X-API-Key"); raw != "" {
		return raw, true
	}
	return "", false
}

// ipAllowed reports whether addr falls inside any of the canonical CIDR strings.
// An invalid addr (indeterminate client IP) is denied; a malformed stored entry
// is skipped — both fail closed.
func ipAllowed(addr netip.Addr, cidrs []string) bool {
	if !addr.IsValid() {
		return false
	}
	for _, c := range cidrs {
		p, err := netip.ParsePrefix(c)
		if err != nil {
			continue
		}
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// createdByString renders a nullable creator id as a string ("" when NULL), for
// the Principal's UserID. An api key acts as its workspace; the creator is
// informational.
func createdByString(v pgtype.UUID) string {
	if !v.Valid {
		return ""
	}
	return uuid.UUID(v.Bytes).String()
}

// Compile-time proof the verifier satisfies the auth seam.
var _ auth.Verifier = (*Verifier)(nil)
