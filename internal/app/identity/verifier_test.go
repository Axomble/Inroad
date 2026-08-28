package identity

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/inroad/inroad/internal/app/auth"
)

// fakeAuthStore is an in-memory sessionAuthStore that also counts lookups, so
// tests can assert the verifier's cache actually elides DB reads.
type fakeAuthStore struct {
	states map[uuid.UUID]SessionAuthState
	calls  int
	err    error
}

func (f *fakeAuthStore) GetSessionAuthState(_ context.Context, sid uuid.UUID) (SessionAuthState, error) {
	f.calls++
	if f.err != nil {
		return SessionAuthState{}, f.err
	}
	st, ok := f.states[sid]
	if !ok {
		return SessionAuthState{}, pgx.ErrNoRows
	}
	return st, nil
}

var verifierSecret = []byte("verifier-secret-verifier-secret")

// bearerRequest mints an access token with the given claims and returns a
// request carrying it as a Bearer credential.
func bearerRequest(t *testing.T, c auth.Claims) *http.Request {
	t.Helper()
	tok, err := auth.IssueToken(verifierSecret, c, time.Minute)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/x", http.NoBody)
	r.Header.Set("Authorization", "Bearer "+tok)
	return r
}

func liveState(userID uuid.UUID) SessionAuthState {
	return SessionAuthState{UserID: userID, Revoked: false, ExpiresAt: time.Now().Add(time.Hour), TokenVersion: 0}
}

func TestSessionVerifierAcceptsLiveSession(t *testing.T) {
	uid, sid := uuid.New(), uuid.New()
	store := &fakeAuthStore{states: map[uuid.UUID]SessionAuthState{sid: liveState(uid)}}
	v := NewSessionVerifier(verifierSecret, store, 0)

	r := bearerRequest(t, auth.Claims{UserID: uid.String(), WorkspaceID: uuid.NewString(), Role: "owner", SessionID: sid.String(), TokenVersion: 0})
	p, ok, err := v.Verify(context.Background(), r)
	if !ok || err != nil {
		t.Fatalf("live session should authenticate, got ok=%v err=%v", ok, err)
	}
	if p.Kind != auth.KindSession || p.UserID != uid.String() || p.SessionID != sid.String() {
		t.Fatalf("principal mismatch: %+v", p)
	}
}

func TestSessionVerifierRejectsRevoked(t *testing.T) {
	uid, sid := uuid.New(), uuid.New()
	st := liveState(uid)
	st.Revoked = true
	store := &fakeAuthStore{states: map[uuid.UUID]SessionAuthState{sid: st}}
	v := NewSessionVerifier(verifierSecret, store, 0)

	r := bearerRequest(t, auth.Claims{UserID: uid.String(), SessionID: sid.String()})
	_, ok, err := v.Verify(context.Background(), r)
	if ok || !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("revoked session must reject with ErrUnauthorized, got ok=%v err=%v", ok, err)
	}
}

func TestSessionVerifierRejectsTokenVersionMismatch(t *testing.T) {
	uid, sid := uuid.New(), uuid.New()
	st := liveState(uid)
	st.TokenVersion = 1 // session bumped past the token's tv
	store := &fakeAuthStore{states: map[uuid.UUID]SessionAuthState{sid: st}}
	v := NewSessionVerifier(verifierSecret, store, 0)

	r := bearerRequest(t, auth.Claims{UserID: uid.String(), SessionID: sid.String(), TokenVersion: 0})
	_, ok, err := v.Verify(context.Background(), r)
	if ok || !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("stale token_version must reject, got ok=%v err=%v", ok, err)
	}
}

func TestSessionVerifierRejectsExpiredSession(t *testing.T) {
	uid, sid := uuid.New(), uuid.New()
	st := liveState(uid)
	st.ExpiresAt = time.Now().Add(-time.Minute)
	store := &fakeAuthStore{states: map[uuid.UUID]SessionAuthState{sid: st}}
	v := NewSessionVerifier(verifierSecret, store, 0)

	r := bearerRequest(t, auth.Claims{UserID: uid.String(), SessionID: sid.String()})
	_, ok, err := v.Verify(context.Background(), r)
	if ok || !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("expired session must reject, got ok=%v err=%v", ok, err)
	}
}

func TestSessionVerifierRejectsSubMismatch(t *testing.T) {
	uid, sid := uuid.New(), uuid.New()
	store := &fakeAuthStore{states: map[uuid.UUID]SessionAuthState{sid: liveState(uid)}}
	v := NewSessionVerifier(verifierSecret, store, 0)

	// Token's sub is a different user than the session's owner.
	r := bearerRequest(t, auth.Claims{UserID: uuid.NewString(), SessionID: sid.String()})
	_, ok, err := v.Verify(context.Background(), r)
	if ok || !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("sub/owner mismatch must reject, got ok=%v err=%v", ok, err)
	}
}

func TestSessionVerifierRejectsUnknownSession(t *testing.T) {
	store := &fakeAuthStore{states: map[uuid.UUID]SessionAuthState{}}
	v := NewSessionVerifier(verifierSecret, store, 0)

	r := bearerRequest(t, auth.Claims{UserID: uuid.NewString(), SessionID: uuid.NewString()})
	_, ok, err := v.Verify(context.Background(), r)
	if ok || !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("unknown session must reject, got ok=%v err=%v", ok, err)
	}
}

func TestSessionVerifierPropagatesStoreError(t *testing.T) {
	store := &fakeAuthStore{err: errors.New("db down")}
	v := NewSessionVerifier(verifierSecret, store, 0)

	r := bearerRequest(t, auth.Claims{UserID: uuid.NewString(), SessionID: uuid.NewString()})
	_, ok, err := v.Verify(context.Background(), r)
	if ok {
		t.Fatal("store error must not authenticate")
	}
	if err == nil || errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("store error should surface as a non-Unauthorized error (500), got %v", err)
	}
}

func TestSessionVerifierDefersWithoutBearer(t *testing.T) {
	store := &fakeAuthStore{states: map[uuid.UUID]SessionAuthState{}}
	v := NewSessionVerifier(verifierSecret, store, 0)
	_, ok, err := v.Verify(context.Background(), httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/x", http.NoBody))
	if ok || err != nil {
		t.Fatalf("no bearer should defer (false,nil), got ok=%v err=%v", ok, err)
	}
	if store.calls != 0 {
		t.Fatalf("deferring must not hit the store, got %d calls", store.calls)
	}
}

// TestSessionVerifierCacheElidesLookupsAndBustReloads proves the cache serves
// within its TTL (one DB read for repeated requests) and that Bust forces a
// re-read that observes a revocation applied out of band.
func TestSessionVerifierCacheElidesLookupsAndBustReloads(t *testing.T) {
	uid, sid := uuid.New(), uuid.New()
	st := liveState(uid)
	store := &fakeAuthStore{states: map[uuid.UUID]SessionAuthState{sid: st}}
	v := NewSessionVerifier(verifierSecret, store, time.Minute)
	claims := auth.Claims{UserID: uid.String(), SessionID: sid.String()}

	if _, ok, err := v.Verify(context.Background(), bearerRequest(t, claims)); !ok || err != nil {
		t.Fatalf("first verify should pass, ok=%v err=%v", ok, err)
	}
	if _, ok, _ := v.Verify(context.Background(), bearerRequest(t, claims)); !ok {
		t.Fatal("second verify should pass from cache")
	}
	if store.calls != 1 {
		t.Fatalf("cache should elide the second lookup, got %d store calls", store.calls)
	}

	// Revoke out of band: the cache still serves the stale "live" state...
	revoked := st
	revoked.Revoked = true
	store.states[sid] = revoked
	if _, ok, _ := v.Verify(context.Background(), bearerRequest(t, claims)); !ok {
		t.Fatal("within TTL the cache still serves the pre-revoke state")
	}

	// ...until Bust forces a reload, which now observes the revocation.
	v.Bust(sid)
	if _, ok, err := v.Verify(context.Background(), bearerRequest(t, claims)); ok || !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("after Bust the revoke must take effect, got ok=%v err=%v", ok, err)
	}
}

// SessionLive is the realtime handshake's revocation check: a WebSocket
// authenticates with a signed connect ticket rather than a bearer token, and
// must still refuse a session logged out between minting that ticket and
// spending it. These cover the branches the socket depends on.

func TestSessionLive_TrueForALiveSession(t *testing.T) {
	sid := uuid.New()
	store := &fakeAuthStore{states: map[uuid.UUID]SessionAuthState{
		sid: {UserID: uuid.New(), ExpiresAt: time.Now().Add(time.Hour)},
	}}
	v := NewSessionVerifier(verifierSecret, store, 0)

	live, err := v.SessionLive(context.Background(), sid.String())
	if err != nil {
		t.Fatalf("SessionLive: %v", err)
	}
	if !live {
		t.Error("live = false, want true")
	}
}

// The case the whole method exists for: the socket must not open after a logout.
func TestSessionLive_FalseForARevokedSession(t *testing.T) {
	sid := uuid.New()
	store := &fakeAuthStore{states: map[uuid.UUID]SessionAuthState{
		sid: {UserID: uuid.New(), ExpiresAt: time.Now().Add(time.Hour), Revoked: true},
	}}
	v := NewSessionVerifier(verifierSecret, store, 0)

	live, err := v.SessionLive(context.Background(), sid.String())
	if err != nil {
		t.Fatalf("SessionLive: %v", err)
	}
	if live {
		t.Error("live = true for a revoked session; a logged-out user could open a socket")
	}
}

func TestSessionLive_FalseForAnExpiredSession(t *testing.T) {
	sid := uuid.New()
	store := &fakeAuthStore{states: map[uuid.UUID]SessionAuthState{
		sid: {UserID: uuid.New(), ExpiresAt: time.Now().Add(-time.Second)},
	}}
	v := NewSessionVerifier(verifierSecret, store, 0)

	live, err := v.SessionLive(context.Background(), sid.String())
	if err != nil {
		t.Fatalf("SessionLive: %v", err)
	}
	if live {
		t.Error("live = true for an expired session")
	}
}

// An unknown session and a malformed id are both definite "no"s, not failures to
// determine liveness — so neither may surface as an error the caller might treat
// as a transient outage.
func TestSessionLive_UnknownAndMalformedAreDefiniteNo(t *testing.T) {
	v := NewSessionVerifier(verifierSecret, &fakeAuthStore{states: map[uuid.UUID]SessionAuthState{}}, 0)

	for _, id := range []string{uuid.NewString(), "not-a-uuid", ""} {
		live, err := v.SessionLive(context.Background(), id)
		if err != nil {
			t.Errorf("SessionLive(%q) err = %v, want nil", id, err)
		}
		if live {
			t.Errorf("SessionLive(%q) = true, want false", id)
		}
	}
}

// A store outage must FAIL CLOSED. Returning (false, nil) here would be worse
// than the error: the handshake would read it as a definite "not live" and, more
// dangerously, a future refactor could invert it into an open socket.
func TestSessionLive_PropagatesAStoreFailure(t *testing.T) {
	wantErr := errors.New("postgres is down")
	v := NewSessionVerifier(verifierSecret, &fakeAuthStore{err: wantErr}, 0)

	live, err := v.SessionLive(context.Background(), uuid.NewString())
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want %v", err, wantErr)
	}
	if live {
		t.Error("live = true despite a store failure")
	}
}

// SessionLive shares `load` with Verify, so it inherits the same cache — and a
// Bust must be visible to the socket path too, or a revoke would keep sockets
// alive for the cache TTL.
func TestSessionLive_ObservesBust(t *testing.T) {
	sid := uuid.New()
	states := map[uuid.UUID]SessionAuthState{
		sid: {UserID: uuid.New(), ExpiresAt: time.Now().Add(time.Hour)},
	}
	store := &fakeAuthStore{states: states}
	v := NewSessionVerifier(verifierSecret, store, time.Minute) // caching ON

	if live, err := v.SessionLive(context.Background(), sid.String()); err != nil || !live {
		t.Fatalf("first call: live=%v err=%v, want true/nil", live, err)
	}

	// Revoke out of band, then bust as the session-management path does.
	states[sid] = SessionAuthState{UserID: states[sid].UserID, ExpiresAt: states[sid].ExpiresAt, Revoked: true}
	v.Bust(sid)

	if live, err := v.SessionLive(context.Background(), sid.String()); err != nil || live {
		t.Errorf("after Bust: live=%v err=%v, want false/nil", live, err)
	}
}
