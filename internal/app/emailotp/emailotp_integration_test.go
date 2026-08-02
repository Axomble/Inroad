//go:build integration

package emailotp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/identity"
	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/dbtest"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/notify"
)

var itSecret = []byte("emailotp-it-secret-emailotp-its")

const (
	itAccessTTL  = 15 * time.Minute
	itRefreshTTL = 30 * 24 * time.Hour
)

var itCodeInEmail = regexp.MustCompile(`\d{6}`)

// itSender captures delivered messages so a test can read the emailed code.
type itSender struct {
	mu   sync.Mutex
	sent []notify.Message
}

func (s *itSender) Send(_ context.Context, m notify.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, m)
	return nil
}

func (s *itSender) lastCode(t *testing.T) string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sent) == 0 {
		t.Fatal("no login-code email delivered")
	}
	code := itCodeInEmail.FindString(s.sent[len(s.sent)-1].TextBody)
	if code == "" {
		t.Fatalf("no code in email %q", s.sent[len(s.sent)-1].TextBody)
	}
	return code
}

// itEnv wires identity + emailotp against a real Postgres, mirroring cmd/inroad
// (gate nil, so a successful OTP verify mints a session directly). It seeds a
// workspace + user + membership so StartSessionForUser has an active workspace.
type itEnv struct {
	srv   *httptest.Server
	svc   *Service // exposed so a test can advance the clock (expiry)
	send  *itSender
	email string
	uid   uuid.UUID
}

func newItEnv(t *testing.T) *itEnv {
	t.Helper()
	ctx := context.Background()
	if err := db.Migrate(dbtest.DSN(t)); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := db.Connect(ctx, dbtest.DSN(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, "TRUNCATE email_otp_codes"); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	q := gen.New(pool)
	email := "otp-" + uuid.NewString() + "@example.test"
	ws, err := q.CreateWorkspace(ctx, "otp-it-ws")
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	user, err := q.CreateUser(ctx, gen.CreateUserParams{Email: email, PasswordHash: "x"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := q.CreateMember(ctx, gen.CreateMemberParams{WorkspaceID: ws.ID, UserID: user.ID, Role: gen.MemberRoleOwner}); err != nil {
		t.Fatalf("create member: %v", err)
	}

	identStore := identity.NewStore(pool)
	verifier := identity.NewSessionVerifier(itSecret, identStore, 0)
	identSvc := identity.NewService(identStore, itRefreshTTL, &itSender{}, "https://app.example.test", time.Hour, time.Hour, time.Hour)
	identHandler := identity.NewHandler(identSvc, itSecret, itAccessTTL, itRefreshTTL, false, "", nil, verifier, nil)

	send := &itSender{}
	svc := NewService(NewPgStore(pool), send)
	svc.dispatch = func(f func()) { f() } // deterministic: no goroutine
	otpHandler := NewHandler(svc, identHandler)

	r := chi.NewRouter()
	r.Mount("/api/v1/auth", identHandler.Routes(identity.RouteDeps{Verifier: verifier, EmailOTP: otpHandler.Routes(nil, nil)}))
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	return &itEnv{srv: srv, svc: svc, send: send, email: email, uid: user.ID}
}

func (e *itEnv) post(t *testing.T, path string, body any) (int, map[string]any) {
	t.Helper()
	buf, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, e.srv.URL+path, bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// TestIntegrationStartVerifySession runs the full round-trip: start emails a code,
// verify exchanges it for a session (access_token in the body).
func TestIntegrationStartVerifySession(t *testing.T) {
	e := newItEnv(t)
	if code, _ := e.post(t, "/api/v1/auth/email-otp/start", map[string]string{"email": e.email}); code != http.StatusOK {
		t.Fatalf("start: got %d, want 200", code)
	}
	otp := e.send.lastCode(t)

	status, body := e.post(t, "/api/v1/auth/email-otp/verify", map[string]string{"email": e.email, "code": otp})
	if status != http.StatusOK {
		t.Fatalf("verify: got %d, want 200", status)
	}
	if _, ok := body["access_token"].(string); !ok {
		t.Fatalf("verify response has no access_token: %v", body)
	}
}

// TestIntegrationReplayFails proves a consumed code cannot be reused.
func TestIntegrationReplayFails(t *testing.T) {
	e := newItEnv(t)
	_, _ = e.post(t, "/api/v1/auth/email-otp/start", map[string]string{"email": e.email})
	otp := e.send.lastCode(t)

	if status, _ := e.post(t, "/api/v1/auth/email-otp/verify", map[string]string{"email": e.email, "code": otp}); status != http.StatusOK {
		t.Fatalf("first verify: got %d, want 200", status)
	}
	if status, _ := e.post(t, "/api/v1/auth/email-otp/verify", map[string]string{"email": e.email, "code": otp}); status != http.StatusUnauthorized {
		t.Fatalf("replay: got %d, want 401", status)
	}
}

// TestIntegrationExpiredFails proves a code past its TTL is rejected.
func TestIntegrationExpiredFails(t *testing.T) {
	e := newItEnv(t)
	_, _ = e.post(t, "/api/v1/auth/email-otp/start", map[string]string{"email": e.email})
	otp := e.send.lastCode(t)

	e.svc.now = func() time.Time { return time.Now().Add(codeTTL + time.Minute) }
	if status, _ := e.post(t, "/api/v1/auth/email-otp/verify", map[string]string{"email": e.email, "code": otp}); status != http.StatusUnauthorized {
		t.Fatalf("expired: got %d, want 401", status)
	}
}

// TestIntegrationAttemptCap proves a code dies after maxAttempts wrong guesses —
// even the correct code no longer works.
func TestIntegrationAttemptCap(t *testing.T) {
	e := newItEnv(t)
	_, _ = e.post(t, "/api/v1/auth/email-otp/start", map[string]string{"email": e.email})
	otp := e.send.lastCode(t)
	wrong := "000000"
	if wrong == otp {
		wrong = "111111"
	}

	for i := int32(0); i < maxAttempts; i++ {
		if status, _ := e.post(t, "/api/v1/auth/email-otp/verify", map[string]string{"email": e.email, "code": wrong}); status != http.StatusUnauthorized {
			t.Fatalf("wrong guess %d: got %d, want 401", i, status)
		}
	}
	if status, _ := e.post(t, "/api/v1/auth/email-otp/verify", map[string]string{"email": e.email, "code": otp}); status != http.StatusUnauthorized {
		t.Fatalf("correct code after cap: got %d, want 401", status)
	}
}
