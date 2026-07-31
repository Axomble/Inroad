package identity

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/inroad/inroad/internal/app/auth"
)

// SessionAuthState is the minimal per-request validation state for a session,
// in plain Go types (no pgx). The verifier decides a request from exactly
// these four facts.
type SessionAuthState struct {
	UserID       uuid.UUID
	Revoked      bool
	ExpiresAt    time.Time
	TokenVersion int
}

// sessionAuthStore is the one method the verifier needs from the store
// (consumer-defined, dependency-inverted): a cheap primary-key lookup of a
// session's auth state. *Store satisfies it.
type sessionAuthStore interface {
	GetSessionAuthState(ctx context.Context, sid uuid.UUID) (SessionAuthState, error)
}

// SessionVerifier is the store-backed auth.Verifier that makes access tokens
// revocable: it validates the HS256 Bearer token (alg-pinned, via
// auth.ParseToken), then loads the session by `sid` and rejects it if the
// session is unknown, revoked, expired, its owner disagrees with the token's
// `sub`, or its persisted token_version has moved past the token's `tv`.
//
// A short-TTL in-process cache fronts the primary-key lookup to keep the
// per-request cost near zero. CORRECTNESS NOTE: a revoke or token_version bump
// performed IN THIS PROCESS is made prompt by Bust (called from the
// session-management + security-event paths). A change made out-of-band (a
// second API replica, or the password-reset tx which revokes an unknown set of
// sessions) propagates within at most the cache TTL — bounded, and acceptable
// because the short access TTL plus refresh-family/cookie revocation already
// stop any NEW token from being minted. Set the TTL to <= 0 to disable the
// cache entirely (every request hits the DB); integration tests do this so
// revocation assertions are deterministic.
type SessionVerifier struct {
	secret []byte
	store  sessionAuthStore
	cache  *sessionCache
}

// NewSessionVerifier builds a SessionVerifier over store, parsing tokens with
// secret and fronting lookups with a cache of the given TTL (<=0 disables it).
func NewSessionVerifier(secret []byte, store sessionAuthStore, cacheTTL time.Duration) *SessionVerifier {
	return &SessionVerifier{secret: secret, store: store, cache: newSessionCache(cacheTTL)}
}

// Verify implements auth.Verifier. See the type doc for the rejection rules.
func (v *SessionVerifier) Verify(ctx context.Context, r *http.Request) (auth.Principal, bool, error) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return auth.Principal{}, false, nil // no Bearer credential: defer
	}
	claims, err := auth.ParseToken(v.secret, token)
	if err != nil {
		return auth.Principal{}, false, auth.ErrUnauthorized // presented but invalid
	}
	sid, err := uuid.Parse(claims.SessionID)
	if err != nil {
		return auth.Principal{}, false, auth.ErrUnauthorized
	}
	sub, err := uuid.Parse(claims.UserID)
	if err != nil {
		return auth.Principal{}, false, auth.ErrUnauthorized
	}

	st, err := v.load(ctx, sid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return auth.Principal{}, false, auth.ErrUnauthorized // unknown session
		}
		return auth.Principal{}, false, err // store failure -> 500 (fail loud)
	}

	switch {
	case st.UserID != sub: // token's sub disagrees with the session's owner
		return auth.Principal{}, false, auth.ErrUnauthorized
	case st.Revoked:
		return auth.Principal{}, false, auth.ErrUnauthorized
	case time.Now().After(st.ExpiresAt):
		return auth.Principal{}, false, auth.ErrUnauthorized
	case st.TokenVersion != claims.TokenVersion:
		return auth.Principal{}, false, auth.ErrUnauthorized
	}
	return auth.PrincipalFromClaims(claims), true, nil
}

// Bust drops any cached auth-state for sid so the next request re-reads it from
// the store. Called after an in-process revoke / token_version bump.
func (v *SessionVerifier) Bust(sid uuid.UUID) { v.cache.bust(sid) }

// load returns sid's auth state, serving a fresh cache entry when present and
// otherwise reading through to the store and caching the result.
func (v *SessionVerifier) load(ctx context.Context, sid uuid.UUID) (SessionAuthState, error) {
	if st, ok := v.cache.get(sid); ok {
		return st, nil
	}
	st, err := v.store.GetSessionAuthState(ctx, sid)
	if err != nil {
		return SessionAuthState{}, err
	}
	v.cache.put(sid, st)
	return st, nil
}

// sessionCache is a tiny mutex-guarded TTL cache of session auth-state keyed by
// session id. A ttl <= 0 disables it (get always misses, put is a no-op), so
// the verifier reads straight through to the store.
type sessionCache struct {
	ttl time.Duration
	mu  sync.Mutex
	m   map[uuid.UUID]cachedState
}

type cachedState struct {
	st  SessionAuthState
	exp time.Time
}

func newSessionCache(ttl time.Duration) *sessionCache {
	return &sessionCache{ttl: ttl, m: make(map[uuid.UUID]cachedState)}
}

func (c *sessionCache) get(sid uuid.UUID) (SessionAuthState, bool) {
	if c.ttl <= 0 {
		return SessionAuthState{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[sid]
	if !ok || time.Now().After(e.exp) {
		if ok {
			delete(c.m, sid) // drop the stale entry so the map can't grow unbounded
		}
		return SessionAuthState{}, false
	}
	return e.st, true
}

func (c *sessionCache) put(sid uuid.UUID, st SessionAuthState) {
	if c.ttl <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[sid] = cachedState{st: st, exp: time.Now().Add(c.ttl)}
}

func (c *sessionCache) bust(sid uuid.UUID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, sid)
}
