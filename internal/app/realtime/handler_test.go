package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	platformrealtime "github.com/inroad/inroad/internal/platform/realtime"
	"github.com/inroad/inroad/internal/platform/wsticket"
)

var (
	testSecret    = []byte("test-secret-key-at-least-16")
	testWorkspace = uuid.MustParse("8f14e45f-ceea-467e-adc1-0000000000ab")
	testUser      = uuid.MustParse("3c59dc04-8e88-4e53-a8f4-0000000000cd")
	testSession   = "b6d767d2-f8ed-4f57-a8ff-0000000000ef"
	testOrigin    = "https://app.example.com"
)

// --- doubles ---------------------------------------------------------------

// fakeBurner records burnt nonces in memory. burnErr drives the "store is down"
// branch, which must fail CLOSED.
type fakeBurner struct {
	mu      sync.Mutex
	burnt   map[string]bool
	burnErr error
}

func newFakeBurner() *fakeBurner { return &fakeBurner{burnt: map[string]bool{}} }

func (b *fakeBurner) Burn(_ context.Context, nonce string, ttl time.Duration) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.burnErr != nil {
		return false, b.burnErr
	}
	if ttl <= 0 {
		return false, errors.New("non-positive ttl")
	}
	if b.burnt[nonce] {
		return false, nil
	}
	b.burnt[nonce] = true
	return true, nil
}

type fakeSessions struct {
	live bool
	err  error
}

func (s fakeSessions) SessionLive(context.Context, string) (bool, error) { return s.live, s.err }

// fakeHub yields nothing and never blocks the handshake. attachErr drives the
// "hub unavailable" branch, which must be reported BEFORE the upgrade.
type fakeHub struct {
	attachErr error
	attached  int
	mu        sync.Mutex
}

func (h *fakeHub) Attach(context.Context, uuid.UUID, int64) (<-chan platformrealtime.Frame, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.attachErr != nil {
		return nil, h.attachErr
	}
	h.attached++
	ch := make(chan platformrealtime.Frame)
	return ch, nil
}

func (h *fakeHub) attachCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.attached
}

// --- helpers ---------------------------------------------------------------

func testHandler(t *testing.T, hub Attacher, burner TicketBurner, sessions SessionChecker) *Handler {
	t.Helper()
	return NewHandler(hub, burner, sessions, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		Secret:         testSecret,
		AllowedOrigins: []string{testOrigin},
	})
}

// sessionVerifier authenticates every request as the session principal the
// protected group would resolve. Injecting through the REAL auth.RequireAuth
// (the convention in this repo's handler tests, e.g. crm/handler_test.go)
// rather than stuffing the context directly: auth's context key is unexported,
// and routing through the middleware is what makes the anonymous-rejection test
// below meaningful.
type sessionVerifier struct{ sessionID string }

func (v sessionVerifier) Verify(context.Context, *http.Request) (auth.Principal, bool, error) {
	return auth.Principal{
		UserID:      testUser.String(),
		WorkspaceID: testWorkspace.String(),
		Role:        "member",
		SessionID:   v.sessionID,
		Kind:        auth.KindSession,
	}, true, nil
}

// anonymousVerifier declines every request, as the session verifier does for a
// caller with no bearer token.
type anonymousVerifier struct{}

func (anonymousVerifier) Verify(context.Context, *http.Request) (auth.Principal, bool, error) {
	return auth.Principal{}, false, nil
}

// router mounts the handler behind RequireAuth, the way cmd/inroad does.
func router(h *Handler, v auth.Verifier, throttle func(http.Handler) http.Handler) http.Handler {
	r := chi.NewRouter()
	r.Use(auth.RequireAuth(v))
	r.Mount("/realtime", h.Routes(throttle))
	return r
}

// authed is the default router: a live session principal, no throttle.
func authed(h *Handler) http.Handler {
	return router(h, sessionVerifier{sessionID: testSession}, nil)
}

func mintedTicket(t *testing.T, h *Handler) string {
	t.Helper()
	rec := httptest.NewRecorder()
	authed(h).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/realtime/ticket", http.NoBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("mint: got %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var body ticketResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("mint: decode: %v", err)
	}
	if body.Ticket == "" {
		t.Fatal("mint: empty ticket")
	}
	return body.Ticket
}

// wsRequest builds an Upgrade request. It is NOT a real handshake — httptest's
// recorder cannot hijack — so these tests assert the REFUSALS, which all happen
// before the upgrade. The accept path is covered by the integration test below.
func wsRequest(ticket, origin string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/realtime/ws?ticket="+ticket, http.NoBody)
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	r.Header.Set("Connection", "Upgrade")
	r.Header.Set("Upgrade", "websocket")
	r.Header.Set("Sec-WebSocket-Version", "13")
	r.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	return r
}

// --- ticket minting --------------------------------------------------------

func TestMintTicket_ReturnsAParsableTicketForThePrincipalsWorkspace(t *testing.T) {
	h := testHandler(t, &fakeHub{}, newFakeBurner(), fakeSessions{live: true})

	raw := mintedTicket(t, h)

	tk, err := wsticket.Parse(testSecret, raw, time.Now())
	if err != nil {
		t.Fatalf("Parse minted ticket: %v", err)
	}
	// The workspace comes from the verified principal, never from the request.
	if tk.WorkspaceID != testWorkspace.String() {
		t.Errorf("WorkspaceID = %q, want %q", tk.WorkspaceID, testWorkspace)
	}
	if tk.SessionID != testSession {
		t.Errorf("SessionID = %q, want %q", tk.SessionID, testSession)
	}
	if tk.Nonce == "" {
		t.Error("minted ticket has no nonce — it could never be made single-use")
	}
}

func TestMintTicket_RejectsAnUnauthenticatedRequest(t *testing.T) {
	h := testHandler(t, &fakeHub{}, newFakeBurner(), fakeSessions{live: true})

	rec := httptest.NewRecorder()
	router(h, anonymousVerifier{}, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/realtime/ticket", http.NoBody))

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("got %d, want 401", rec.Code)
	}
}

// A principal with no session id cannot be revoked between mint and connect, so
// the socket's logout re-check would be meaningless. Refuse rather than mint.
func TestMintTicket_RejectsAPrincipalWithNoSession(t *testing.T) {
	h := testHandler(t, &fakeHub{}, newFakeBurner(), fakeSessions{live: true})

	rec := httptest.NewRecorder()
	// A principal with no session id — the shape an api-key or OAuth caller has.
	router(h, sessionVerifier{sessionID: ""}, nil).
		ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/realtime/ticket", http.NoBody))

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("got %d, want 401", rec.Code)
	}
}

func TestMintTicket_EachCallMintsADistinctTicket(t *testing.T) {
	h := testHandler(t, &fakeHub{}, newFakeBurner(), fakeSessions{live: true})

	// Two tabs mint separately; burning one nonce must not invalidate the other.
	// Bound to variables rather than compared inline: two calls to the same
	// function read as identical expressions to staticcheck (SA4000), and naming
	// them also makes the two tickets inspectable when this fails.
	first := mintedTicket(t, h)
	second := mintedTicket(t, h)

	if first == second {
		t.Error("two mints produced the same ticket — burning one would kill both")
	}

	// Specifically: the NONCES differ. Identical tickets would also be caught
	// above, but this names the field that makes single-use survive two tabs.
	firstTicket, err := wsticket.Parse(testSecret, first, time.Now())
	if err != nil {
		t.Fatalf("parse first: %v", err)
	}
	secondTicket, err := wsticket.Parse(testSecret, second, time.Now())
	if err != nil {
		t.Fatalf("parse second: %v", err)
	}
	if firstTicket.Nonce == secondTicket.Nonce {
		t.Errorf("both tickets carry nonce %q — spending one would refuse the other", firstTicket.Nonce)
	}
}

// --- handshake refusals ----------------------------------------------------

func TestServeWS_RejectsAMissingTicket(t *testing.T) {
	h := testHandler(t, &fakeHub{}, newFakeBurner(), fakeSessions{live: true})

	rec := httptest.NewRecorder()
	authed(h).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/realtime/ws", http.NoBody))

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("got %d, want 401", rec.Code)
	}
}

func TestServeWS_RejectsAGarbageTicket(t *testing.T) {
	h := testHandler(t, &fakeHub{}, newFakeBurner(), fakeSessions{live: true})

	rec := httptest.NewRecorder()
	authed(h).ServeHTTP(rec, wsRequest("not-a-ticket", testOrigin))

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("got %d, want 401", rec.Code)
	}
}

// A ticket signed with a different secret must not open a socket — this is the
// forgery case, and the workspace inside it is attacker-chosen.
func TestServeWS_RejectsATicketSignedWithAnotherSecret(t *testing.T) {
	h := testHandler(t, &fakeHub{}, newFakeBurner(), fakeSessions{live: true})

	forged := wsticket.Make([]byte("a-completely-different-secret"), wsticket.Ticket{
		WorkspaceID: "00000000-0000-0000-0000-00000000dead",
		UserID:      testUser.String(),
		SessionID:   testSession,
		ExpiresAt:   time.Now().Add(time.Minute),
		Nonce:       "forged-nonce",
	})
	rec := httptest.NewRecorder()
	authed(h).ServeHTTP(rec, wsRequest(forged, testOrigin))

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("got %d, want 401", rec.Code)
	}
}

func TestServeWS_RejectsAnExpiredTicket(t *testing.T) {
	h := testHandler(t, &fakeHub{}, newFakeBurner(), fakeSessions{live: true})

	expired := wsticket.Make(testSecret, wsticket.Ticket{
		WorkspaceID: testWorkspace.String(),
		UserID:      testUser.String(),
		SessionID:   testSession,
		ExpiresAt:   time.Now().Add(-time.Second),
		Nonce:       "stale-nonce",
	})
	rec := httptest.NewRecorder()
	authed(h).ServeHTTP(rec, wsRequest(expired, testOrigin))

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("got %d, want 401", rec.Code)
	}
}

// THE replay test. A ticket in a proxy log or browser history must be worthless
// once spent, which is the entire reason the nonce exists.
func TestServeWS_RefusesAReplayedTicket(t *testing.T) {
	burner := newFakeBurner()
	h := testHandler(t, &fakeHub{}, burner, fakeSessions{live: true})
	ticket := mintedTicket(t, h)

	// First use: gets past every check and reaches the upgrade, which fails only
	// because httptest cannot hijack. What matters is that the nonce was burnt.
	first := httptest.NewRecorder()
	authed(h).ServeHTTP(first, wsRequest(ticket, testOrigin))
	if first.Code == http.StatusUnauthorized {
		t.Fatalf("first use was refused (%d) — the replay assertion below would be vacuous", first.Code)
	}

	second := httptest.NewRecorder()
	authed(h).ServeHTTP(second, wsRequest(ticket, testOrigin))
	if second.Code != http.StatusUnauthorized {
		t.Errorf("replay: got %d, want 401", second.Code)
	}
}

// A logout between minting and connecting must be refused, not honoured for the
// remainder of the TTL.
func TestServeWS_RefusesATicketForADeadSession(t *testing.T) {
	h := testHandler(t, &fakeHub{}, newFakeBurner(), fakeSessions{live: true})
	ticket := mintedTicket(t, h)

	// The session dies after the ticket is minted.
	h.sessions = fakeSessions{live: false}

	rec := httptest.NewRecorder()
	authed(h).ServeHTTP(rec, wsRequest(ticket, testOrigin))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("got %d, want 401", rec.Code)
	}
}

// The session check runs BEFORE the burn, so a refused connect leaves the ticket
// unspent and a retry reports the same reason rather than "already spent".
func TestServeWS_ADeadSessionDoesNotConsumeTheTicket(t *testing.T) {
	burner := newFakeBurner()
	h := testHandler(t, &fakeHub{}, burner, fakeSessions{live: false})
	ticket := mintedTicket(t, h)

	rec := httptest.NewRecorder()
	authed(h).ServeHTTP(rec, wsRequest(ticket, testOrigin))

	burner.mu.Lock()
	burnt := len(burner.burnt)
	burner.mu.Unlock()
	if burnt != 0 {
		t.Errorf("burnt %d nonces on a dead-session refusal, want 0", burnt)
	}
	_ = rec
}

// Both infrastructure failures must fail CLOSED: an outage cannot turn every
// ticket into a reusable credential, nor open an unsubscribed socket.
func TestServeWS_FailsClosedWhenTheBurnerIsDown(t *testing.T) {
	burner := newFakeBurner()
	burner.burnErr = errors.New("redis is down")
	h := testHandler(t, &fakeHub{}, burner, fakeSessions{live: true})
	ticket := mintedTicket(t, h)

	rec := httptest.NewRecorder()
	authed(h).ServeHTTP(rec, wsRequest(ticket, testOrigin))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("got %d, want 503", rec.Code)
	}
}

func TestServeWS_FailsClosedWhenTheSessionCheckErrors(t *testing.T) {
	h := testHandler(t, &fakeHub{}, newFakeBurner(), fakeSessions{err: errors.New("store is down")})
	ticket := mintedTicket(t, h)

	rec := httptest.NewRecorder()
	authed(h).ServeHTTP(rec, wsRequest(ticket, testOrigin))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("got %d, want 503", rec.Code)
	}
}

// Attach must be attempted BEFORE the upgrade: after the 101 the status line is
// spent and a failure can only be reported as a close frame.
func TestServeWS_ReportsHubFailureAsAnHTTPErrorNotACloseFrame(t *testing.T) {
	h := testHandler(t, &fakeHub{attachErr: errors.New("no redis")}, newFakeBurner(), fakeSessions{live: true})
	ticket := mintedTicket(t, h)

	rec := httptest.NewRecorder()
	authed(h).ServeHTTP(rec, wsRequest(ticket, testOrigin))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("got %d, want 503", rec.Code)
	}
}

// --- connection caps -------------------------------------------------------

func TestServeWS_RefusesOverThePerUserCap(t *testing.T) {
	hub := &fakeHub{}
	h := NewHandler(hub, newFakeBurner(), fakeSessions{live: true},
		slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
			Secret:         testSecret,
			AllowedOrigins: []string{testOrigin},
			MaxPerUser:     1,
		})

	// Hold one slot as a live connection would.
	release, err := h.counter.acquire(testWorkspace, testUser)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer release()

	rec := httptest.NewRecorder()
	authed(h).ServeHTTP(rec, wsRequest(mintedTicket(t, h), testOrigin))

	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("got %d, want 429", rec.Code)
	}
	// 429 must be cheap: nothing was subscribed for a refused connection.
	if n := hub.attachCount(); n != 0 {
		t.Errorf("attached %d times on a capped connection, want 0", n)
	}
}

// --- last_seq parsing ------------------------------------------------------

func TestLastSeq(t *testing.T) {
	for _, tc := range []struct {
		query string
		want  int64
	}{
		// Absent means "from now", not "replay everything": a client that has seen
		// nothing gets the live feed, since its initial page load covers history.
		{"", -1},
		{"0", 0},
		{"41", 41},
		{"-1", -1},
		{"abc", -1},
		{"12abc", -1},
		{"99999999999999999999", -1},
	} {
		r := httptest.NewRequest(http.MethodGet, "/realtime/ws?last_seq="+tc.query, http.NoBody)
		if got := lastSeq(r); got != tc.want {
			t.Errorf("lastSeq(%q) = %d, want %d", tc.query, got, tc.want)
		}
	}
}

// --- route shape -----------------------------------------------------------

// The ticket route is throttled because it mints credentials (spec §7.4). This
// asserts the middleware is actually WIRED, not merely accepted as a parameter —
// a nil-safe optional argument that silently does nothing is the failure mode.
func TestRoutes_ThrottleMiddlewareIsAppliedToTicketMinting(t *testing.T) {
	h := testHandler(t, &fakeHub{}, newFakeBurner(), fakeSessions{live: true})

	var called bool
	throttle := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
			next.ServeHTTP(w, r)
		})
	}

	rec := httptest.NewRecorder()
	router(h, sessionVerifier{sessionID: testSession}, throttle).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/realtime/ticket", http.NoBody))

	if !called {
		t.Error("throttle middleware was not applied to POST /ticket")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("got %d, want 200", rec.Code)
	}
}

// The socket route must NOT be throttled: a reconnect storm after a deploy is
// exactly when every client reconnects at once, and 429-ing them would turn a
// rolling restart into an outage. Minting is the credential-issuing step and is
// where the cap belongs.
func TestRoutes_ThrottleIsNotAppliedToTheSocket(t *testing.T) {
	h := testHandler(t, &fakeHub{}, newFakeBurner(), fakeSessions{live: true})

	var called bool
	throttle := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
			next.ServeHTTP(w, r)
		})
	}

	rec := httptest.NewRecorder()
	router(h, sessionVerifier{sessionID: testSession}, throttle).ServeHTTP(rec, wsRequest("bogus", testOrigin))

	if called {
		t.Error("throttle middleware was applied to GET /ws — a reconnect storm would 429")
	}
	_ = rec
}

func TestRoutes_TicketRejectsGET(t *testing.T) {
	h := testHandler(t, &fakeHub{}, newFakeBurner(), fakeSessions{live: true})

	rec := httptest.NewRecorder()
	authed(h).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/realtime/ticket", http.NoBody))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("got %d, want 405", rec.Code)
	}
}

// A refusal must never tell the client WHICH check failed, or the ticket format
// gains a probing oracle. Every rejection body is the same.
func TestServeWS_RefusalBodiesAreIndistinguishable(t *testing.T) {
	h := testHandler(t, &fakeHub{}, newFakeBurner(), fakeSessions{live: true})

	expired := wsticket.Make(testSecret, wsticket.Ticket{
		WorkspaceID: testWorkspace.String(), UserID: testUser.String(), SessionID: testSession,
		ExpiresAt: time.Now().Add(-time.Second), Nonce: "n1",
	})
	forged := wsticket.Make([]byte("another-secret-entirely"), wsticket.Ticket{
		WorkspaceID: testWorkspace.String(), UserID: testUser.String(), SessionID: testSession,
		ExpiresAt: time.Now().Add(time.Minute), Nonce: "n2",
	})

	bodies := map[string]string{}
	for name, ticket := range map[string]string{"expired": expired, "forged": forged, "garbage": "xxx"} {
		rec := httptest.NewRecorder()
		authed(h).ServeHTTP(rec, wsRequest(ticket, testOrigin))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: got %d, want 401", name, rec.Code)
		}
		bodies[name] = strings.TrimSpace(rec.Body.String())
	}

	if bodies["expired"] != bodies["forged"] || bodies["forged"] != bodies["garbage"] {
		t.Errorf("refusal bodies differ, leaking which check failed: %#v", bodies)
	}
}
