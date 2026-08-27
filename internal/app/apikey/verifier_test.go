package apikey

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// fakeLimiter is an injectable RateLimiter for verifier tests.
type fakeLimiter struct {
	allow bool
	err   error
	keys  []string
}

func (f *fakeLimiter) Allow(_ context.Context, key string, _ int, _ time.Duration) (bool, error) {
	f.keys = append(f.keys, key)
	return f.allow, f.err
}

var fixedNow = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

// seedKey mints a fresh token and stores a matching key, applying mut to tune it.
// It returns the raw token to present in a request.
func seedKey(t *testing.T, store *fakeStore, mut func(*gen.ApiKey)) string {
	t.Helper()
	prefix, token, hash, err := newToken()
	if err != nil {
		t.Fatalf("newToken: %v", err)
	}
	key := gen.ApiKey{
		ID:              uuid.New(),
		WorkspaceID:     uuid.New(),
		CreatedByUserID: pgtype.UUID{Bytes: uuid.New(), Valid: true},
		Prefix:          prefix,
		SecretHash:      hash,
		Scopes:          []string{auth.ScopeContactsRead},
	}
	if mut != nil {
		mut(&key)
	}
	store.byPrefix[prefix] = key
	return token
}

func newTestVerifier(store verifierStore, limiter RateLimiter) *Verifier {
	v := NewVerifier(store, limiter, nil)
	v.now = func() time.Time { return fixedNow }
	return v
}

func bearerReq(token string) *http.Request {
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/contacts", http.NoBody)
	r.Header.Set("Authorization", "Bearer "+token)
	r.RemoteAddr = "198.51.100.10:5555"
	return r
}

func allowLimiter() *fakeLimiter { return &fakeLimiter{allow: true} }

// TestVerifyDefersNonAPIKey proves a non-inrd credential DEFERS (false, nil) so a
// later verifier (the session verifier) can claim it — a JWT bearer and a request
// with no credential at all.
func TestVerifyDefersNonAPIKey(t *testing.T) {
	v := newTestVerifier(newFakeStore(), allowLimiter())

	jwt := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody)
	jwt.Header.Set("Authorization", "Bearer eyJhbGciOiJIUzI1NiJ9.body.sig")
	if _, ok, err := v.Verify(context.Background(), jwt); ok || err != nil {
		t.Fatalf("jwt: got (ok=%v, err=%v), want defer (false, nil)", ok, err)
	}

	none := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody)
	if _, ok, err := v.Verify(context.Background(), none); ok || err != nil {
		t.Fatalf("no cred: got (ok=%v, err=%v), want defer", ok, err)
	}
}

// TestVerifyMalformedTokenRejected proves an inrd_-scheme token that this verifier
// OWNS but cannot parse is a hard 401 (ErrUnauthorized), not a defer.
func TestVerifyMalformedTokenRejected(t *testing.T) {
	v := newTestVerifier(newFakeStore(), allowLimiter())
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody)
	r.Header.Set("Authorization", "Bearer inrd_garbled")
	_, ok, err := v.Verify(context.Background(), r)
	if ok || !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("got (ok=%v, err=%v), want ErrUnauthorized", ok, err)
	}
}

// TestVerifyUnknownPrefixRejected proves an unknown key fails closed.
func TestVerifyUnknownPrefixRejected(t *testing.T) {
	v := newTestVerifier(newFakeStore(), allowLimiter())
	_, tok, _, _ := newToken() // never seeded into the store
	_, ok, err := v.Verify(context.Background(), bearerReq(tok))
	if ok || !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("got (ok=%v, err=%v), want ErrUnauthorized", ok, err)
	}
}

// TestVerifyWrongSecretRejected proves a right-prefix, wrong-secret token is
// rejected — the constant-time hash compare fails.
func TestVerifyWrongSecretRejected(t *testing.T) {
	store := newFakeStore()
	// Seed a key, then present a DIFFERENT token that happens to reuse the seeded
	// prefix but carries a different secret.
	realToken := seedKey(t, store, nil)
	realPrefix, _, _ := parseToken(realToken)
	_, otherToken, _, _ := newToken()
	_, otherSecret, _ := strings.Cut(strings.TrimPrefix(otherToken, tokenScheme), "_")
	forged := tokenScheme + realPrefix + "_" + otherSecret

	v := newTestVerifier(store, allowLimiter())
	_, ok, err := v.Verify(context.Background(), bearerReq(forged))
	if ok || !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("got (ok=%v, err=%v), want ErrUnauthorized", ok, err)
	}
}

// TestVerifyRevokedRejected and TestVerifyExpiredRejected prove lifecycle
// fail-closed behavior.
func TestVerifyRevokedRejected(t *testing.T) {
	store := newFakeStore()
	tok := seedKey(t, store, func(k *gen.ApiKey) {
		k.RevokedAt = pgtype.Timestamptz{Time: fixedNow.Add(-time.Hour), Valid: true}
	})
	v := newTestVerifier(store, allowLimiter())
	if _, ok, err := v.Verify(context.Background(), bearerReq(tok)); ok || !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("revoked: got (ok=%v, err=%v), want ErrUnauthorized", ok, err)
	}
}

func TestVerifyExpiredRejected(t *testing.T) {
	store := newFakeStore()
	tok := seedKey(t, store, func(k *gen.ApiKey) {
		k.ExpiresAt = pgtype.Timestamptz{Time: fixedNow.Add(-time.Minute), Valid: true}
	})
	v := newTestVerifier(store, allowLimiter())
	if _, ok, err := v.Verify(context.Background(), bearerReq(tok)); ok || !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("expired: got (ok=%v, err=%v), want ErrUnauthorized", ok, err)
	}
}

// TestVerifySuccessMintsPrincipal proves a valid key mints an api-key principal
// carrying the workspace, creator, and exactly the granted scopes — and touches
// last-used asynchronously.
func TestVerifySuccessMintsPrincipal(t *testing.T) {
	store := newFakeStore()
	touched := make(chan uuid.UUID, 1)
	store.touchSignal = touched
	var seededWS uuid.UUID
	tok := seedKey(t, store, func(k *gen.ApiKey) {
		k.Scopes = []string{auth.ScopeContactsRead, auth.ScopeCampaignsSend}
		seededWS = k.WorkspaceID
	})
	v := newTestVerifier(store, allowLimiter())

	p, ok, err := v.Verify(context.Background(), bearerReq(tok))
	if !ok || err != nil {
		t.Fatalf("got (ok=%v, err=%v), want success", ok, err)
	}
	if p.Kind != auth.KindAPIKey {
		t.Fatalf("kind = %v, want KindAPIKey", p.Kind)
	}
	if p.WorkspaceID != seededWS.String() {
		t.Fatalf("workspace = %q, want %q", p.WorkspaceID, seededWS)
	}
	// Scope subset enforcement: holds exactly the granted scopes, not others.
	if !p.HasScope(auth.ScopeContactsRead) || !p.HasScope(auth.ScopeCampaignsSend) {
		t.Fatal("missing a granted scope")
	}
	if p.HasScope(auth.ScopeMailboxesWrite) {
		t.Fatal("holds an ungranted scope")
	}
	select {
	case <-touched:
	case <-time.After(time.Second):
		t.Fatal("last-used touch did not fire")
	}
}

// TestVerifyIPAllowlist proves a request from outside the allowlist is denied and
// one from inside passes.
func TestVerifyIPAllowlist(t *testing.T) {
	store := newFakeStore()
	tok := seedKey(t, store, func(k *gen.ApiKey) {
		k.IpAllowlist = []string{"203.0.113.0/24"}
	})
	v := newTestVerifier(store, allowLimiter())

	miss := bearerReq(tok) // RemoteAddr 198.51.100.10 — outside the allowlist
	if _, ok, err := v.Verify(context.Background(), miss); ok || !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("allowlist miss: got (ok=%v, err=%v), want ErrUnauthorized", ok, err)
	}

	hit := bearerReq(tok)
	hit.RemoteAddr = "203.0.113.42:6000"
	if _, ok, err := v.Verify(context.Background(), hit); !ok || err != nil {
		t.Fatalf("allowlist hit: got (ok=%v, err=%v), want success", ok, err)
	}
}

// TestVerifyRateLimitOverLimit proves an over-cap key yields ErrRateLimited (->429),
// keyed per key id.
func TestVerifyRateLimitOverLimit(t *testing.T) {
	store := newFakeStore()
	five := int32(5)
	var keyID uuid.UUID
	tok := seedKey(t, store, func(k *gen.ApiKey) {
		k.RateLimitPerMin = &five
		keyID = k.ID
	})
	limiter := &fakeLimiter{allow: false}
	v := newTestVerifier(store, limiter)

	_, ok, err := v.Verify(context.Background(), bearerReq(tok))
	if ok || !errors.Is(err, auth.ErrRateLimited) {
		t.Fatalf("over limit: got (ok=%v, err=%v), want ErrRateLimited", ok, err)
	}
	if len(limiter.keys) != 1 || limiter.keys[0] != keyID.String() {
		t.Fatalf("limiter keyed on %v, want [%s]", limiter.keys, keyID)
	}
}

// TestVerifyRateLimitFailsClosed proves a limiter (Redis) error DENIES the request
// — it is neither ErrUnauthorized nor ErrRateLimited, so RequireAuth maps it to a
// loud 500 rather than lifting the cap.
func TestVerifyRateLimitFailsClosed(t *testing.T) {
	store := newFakeStore()
	five := int32(5)
	tok := seedKey(t, store, func(k *gen.ApiKey) { k.RateLimitPerMin = &five })
	limiter := &fakeLimiter{err: errors.New("redis down")}
	v := newTestVerifier(store, limiter)

	_, ok, err := v.Verify(context.Background(), bearerReq(tok))
	if ok {
		t.Fatal("request allowed despite limiter outage (failed open)")
	}
	if err == nil || errors.Is(err, auth.ErrUnauthorized) || errors.Is(err, auth.ErrRateLimited) {
		t.Fatalf("err = %v, want an opaque non-sentinel error (500)", err)
	}
}

// TestVerifyXAPIKeyHeader proves the alternate X-API-Key header is accepted.
func TestVerifyXAPIKeyHeader(t *testing.T) {
	store := newFakeStore()
	tok := seedKey(t, store, nil)
	v := newTestVerifier(store, allowLimiter())

	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody)
	r.Header.Set("X-API-Key", tok)
	r.RemoteAddr = "198.51.100.10:5555"
	if _, ok, err := v.Verify(context.Background(), r); !ok || err != nil {
		t.Fatalf("X-API-Key: got (ok=%v, err=%v), want success", ok, err)
	}
}
