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

// SessionLive reports whether a session is still usable, WITHOUT a bearer token.
//
// It exists for the realtime WebSocket handshake, which authenticates with a
// signed connect ticket rather than an Authorization header (a browser cannot
// set one on `new WebSocket()`) and must still refuse a session that was logged
// out in the seconds between minting that ticket and spending it.
//
// It shares `load` and the liveness rules with Verify rather than restating
// them, so a new revocation condition cannot be added to one path and forgotten
// in the other. It deliberately does NOT check a token version: there is no
// token here to compare against, and the ticket's own 30-second TTL plus this
// revocation check are what bound its validity.
//
// An unknown session is (false, nil) — a definite "no". A store failure is a
// non-nil error, which the caller must treat as a refusal rather than a pass.
func (v *SessionVerifier) SessionLive(ctx context.Context, sessionID string) (bool, error) {
	// A session id that is not a uuid, and an id naming no row, are both
	// definitively "not live" — neither is a failure to determine liveness. They
	// are folded into the same branch as a revoked session so this method has one
	// answer shape and no path that returns a nil error beside a live=false it
	// derived from a non-nil one.
	wellFormed := uuid.Validate(sessionID) == nil
	if !wellFormed {
		return false, nil
	}
	sid := uuid.MustParse(sessionID) // Validate above guarantees this parses.

	st, err := v.load(ctx, sid)
	if errors.Is(err, pgx.ErrNoRows) {
		st, err = SessionAuthState{Revoked: true}, nil
	}
	if err != nil {
		// The store could not be read. Propagate so the caller refuses rather than
		// guesses — a socket must not open because Postgres was briefly down.
		return false, err
	}
	return !st.Revoked && time.Now().Before(st.ExpiresAt), nil
}

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
