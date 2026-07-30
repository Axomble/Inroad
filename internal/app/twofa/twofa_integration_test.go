//go:build integration

package twofa

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/inroad/inroad/internal/app/identity"
	"github.com/inroad/inroad/internal/platform/crypto"
	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/notify"
)

func dsn() string {
	if v := os.Getenv("INROAD_DATABASE_URL"); v != "" {
		return v
	}
	return "postgres://inroad:inroad@localhost:5433/inroad?sslmode=disable"
}

var itSecret = []byte("twofa-it-secret-twofa-it-secret")

const (
	itAccessTTL  = 15 * time.Minute
	itRefreshTTL = 30 * 24 * time.Hour
)

// noopSender satisfies notify.Sender without delivering anything (register mints
// a best-effort verify email we don't care about here).
type noopSender struct{}

func (noopSender) Send(context.Context, notify.Message) error { return nil }

func itMasterKey() []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i*3 + 1)
	}
	return k
}

// testServer wires identity + twofa exactly as cmd/inroad does, against a real
// Postgres pool, with the given verifier cache TTL. It truncates the challenge
// table so the per-IP throttle can't accumulate across runs.
func testServer(t *testing.T, cacheTTL time.Duration) (*httptest.Server, *gen.Queries) {
	t.Helper()
	ctx := context.Background()
	if err := db.Migrate(dsn()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := db.Connect(ctx, dsn())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, "TRUNCATE two_factor_challenges"); err != nil {
		t.Fatalf("truncate challenges: %v", err)
	}

	identStore := identity.NewStore(pool)
	verifier := identity.NewSessionVerifier(itSecret, identStore, cacheTTL)
	identSvc := identity.NewService(identStore, itRefreshTTL, noopSender{}, "https://app.example.test", time.Hour, time.Hour, time.Hour)

	kr, err := crypto.NewServerKeyring(itMasterKey())
	if err != nil {
		t.Fatalf("server keyring: %v", err)
	}
	twofaSvc := NewService(NewPgStore(pool), kr)
	identHandler := identity.NewHandler(identSvc, itSecret, itAccessTTL, itRefreshTTL, false, "", nil, verifier, twofaSvc)
	twofaHandler := NewHandler(twofaSvc, identHandler, identSvc, verifier)

	r := chi.NewRouter()
	r.Mount("/api/v1/auth", identHandler.Routes(verifier, twofaHandler.Routes(verifier)))
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv, gen.New(pool)
}

// call issues a JSON request and returns status + decoded-into-out body. bearer,
// if non-empty, is sent as the access token.
func call(t *testing.T, srv *httptest.Server, method, path, bearer string, body, out any) int {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, srv.URL+path, rd)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("do %s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func uniqueEmail(t *testing.T, prefix string) string {
	t.Helper()
	return fmt.Sprintf("%s-%d@twofa-it.test", prefix, time.Now().UnixNano())
}

type sessionBody struct {
	AccessToken       string `json:"access_token"`
	TwoFactorRequired bool   `json:"two_factor_required"`
	Challenge         string `json:"challenge"`
}

// enrolledUser registers a user, enrolls + confirms TOTP, and returns their
// email, password, the current access token, the raw secret, and recovery codes.
func enrolledUser(t *testing.T, srv *httptest.Server, prefix string) (email, password, access string, secret []byte, recovery []string) {
	t.Helper()
	email = uniqueEmail(t, prefix)
	password = "correct-horse-battery-staple-2fa"

	var reg sessionBody
	if code := call(t, srv, http.MethodPost, "/api/v1/auth/register", "",
		map[string]string{"workspace_name": "Acme", "email": email, "password": password}, &reg); code != http.StatusOK {
		t.Fatalf("register: got %d", code)
	}
	access = reg.AccessToken

	var enroll enrollResponse
	if code := call(t, srv, http.MethodPost, "/api/v1/auth/2fa/totp", access, nil, &enroll); code != http.StatusOK {
		t.Fatalf("enroll: got %d", code)
	}
	secret, err := base32NoPad.DecodeString(enroll.Secret)
	if err != nil {
		t.Fatalf("decode secret: %v", err)
	}

	var conf confirmResponse
	if code := call(t, srv, http.MethodPost, "/api/v1/auth/2fa/totp/confirm", access,
		map[string]string{"code": totpAt(secret, time.Now())}, &conf); code != http.StatusOK {
		t.Fatalf("confirm: got %d", code)
	}
	if len(conf.RecoveryCodes) != recoveryCodeCount {
		t.Fatalf("recovery codes = %d, want %d", len(conf.RecoveryCodes), recoveryCodeCount)
	}
	return email, password, access, secret, conf.RecoveryCodes
}

// TestFullEnrollConfirmLoginVerifyFlow drives the whole happy path:
// register → enroll → confirm → login-returns-challenge → verify → session.
func TestFullEnrollConfirmLoginVerifyFlow(t *testing.T) {
	srv, _ := testServer(t, 0)
	email, password, access, secret, _ := enrolledUser(t, srv, "flow")

	// Status shows enabled with a full set of recovery codes.
	var st statusResponse
	if code := call(t, srv, http.MethodGet, "/api/v1/auth/2fa", access, nil, &st); code != http.StatusOK {
		t.Fatalf("status: got %d", code)
	}
	if !st.TOTPEnabled || st.RecoveryCodesRemaining != recoveryCodeCount {
		t.Fatalf("status = %+v, want enabled/%d", st, recoveryCodeCount)
	}

	// Login now returns a challenge, NOT a session.
	var login sessionBody
	if code := call(t, srv, http.MethodPost, "/api/v1/auth/login", "",
		map[string]string{"email": email, "password": password}, &login); code != http.StatusOK {
		t.Fatalf("login: got %d", code)
	}
	if !login.TwoFactorRequired || login.Challenge == "" {
		t.Fatalf("expected two_factor_required + challenge, got %+v", login)
	}
	if login.AccessToken != "" {
		t.Fatal("gated login must not issue an access token")
	}

	// Verify with the TOTP code issues a real session.
	var verified sessionBody
	if code := call(t, srv, http.MethodPost, "/api/v1/auth/2fa/verify", "",
		map[string]string{"challenge": login.Challenge, "code": totpAt(secret, time.Now())}, &verified); code != http.StatusOK {
		t.Fatalf("verify: got %d", code)
	}
	if verified.AccessToken == "" {
		t.Fatal("a passed 2FA verify must issue an access token")
	}
	// The new session works.
	if code := call(t, srv, http.MethodGet, "/api/v1/auth/2fa", verified.AccessToken, nil, nil); code != http.StatusOK {
		t.Fatalf("session from verify: got %d", code)
	}
	// Replaying the same challenge fails (single-use).
	var replay sessionBody
	if code := call(t, srv, http.MethodPost, "/api/v1/auth/2fa/verify", "",
		map[string]string{"challenge": login.Challenge, "code": totpAt(secret, time.Now())}, &replay); code != http.StatusUnauthorized {
		t.Fatalf("challenge replay: got %d, want 401", code)
	}
}

// TestLoginVerifyWithRecoveryCode proves a recovery code satisfies the gate and
// is single-use.
func TestLoginVerifyWithRecoveryCode(t *testing.T) {
	srv, _ := testServer(t, 0)
	email, password, _, _, recovery := enrolledUser(t, srv, "recovery")

	login := loginChallenge(t, srv, email, password)
	var verified sessionBody
	if code := call(t, srv, http.MethodPost, "/api/v1/auth/2fa/verify", "",
		map[string]string{"challenge": login, "code": recovery[0]}, &verified); code != http.StatusOK {
		t.Fatalf("verify with recovery code: got %d", code)
	}
	if verified.AccessToken == "" {
		t.Fatal("recovery-code verify must issue a session")
	}
	// Same code on a fresh challenge is now dead.
	login2 := loginChallenge(t, srv, email, password)
	if code := call(t, srv, http.MethodPost, "/api/v1/auth/2fa/verify", "",
		map[string]string{"challenge": login2, "code": recovery[0]}, nil); code != http.StatusUnauthorized {
		t.Fatalf("reused recovery code: got %d, want 401", code)
	}
}

// TestVerifyAttemptCap proves a challenge dies after maxChallengeAttempts wrong
// codes — a correct code afterward is rejected.
func TestVerifyAttemptCap(t *testing.T) {
	srv, _ := testServer(t, 0)
	email, password, _, secret, _ := enrolledUser(t, srv, "cap")
	challenge := loginChallenge(t, srv, email, password)

	for i := int32(0); i < maxChallengeAttempts; i++ {
		if code := call(t, srv, http.MethodPost, "/api/v1/auth/2fa/verify", "",
			map[string]string{"challenge": challenge, "code": "000000"}, nil); code != http.StatusUnauthorized {
			t.Fatalf("wrong attempt %d: got %d, want 401", i, code)
		}
	}
	// The correct code no longer works — the challenge is exhausted.
	if code := call(t, srv, http.MethodPost, "/api/v1/auth/2fa/verify", "",
		map[string]string{"challenge": challenge, "code": totpAt(secret, time.Now())}, nil); code != http.StatusUnauthorized {
		t.Fatalf("correct code after cap: got %d, want 401 (dead challenge)", code)
	}
}

// TestDisableRevokesOtherSessionsAndBusts proves disabling 2FA revokes the user's
// OTHER live session and evicts it from the verifier cache (a long cache TTL means
// the prompt 401 can only come from an explicit bust), while the acting session
// stays alive.
func TestDisableRevokesOtherSessionsAndBusts(t *testing.T) {
	srv, _ := testServer(t, time.Hour) // long TTL: only an explicit Bust kills a token promptly
	email, password, sessionA, secret, _ := enrolledUser(t, srv, "disable")

	// Establish a SECOND session via the 2FA login flow.
	challenge := loginChallenge(t, srv, email, password)
	var second sessionBody
	if code := call(t, srv, http.MethodPost, "/api/v1/auth/2fa/verify", "",
		map[string]string{"challenge": challenge, "code": totpAt(secret, time.Now())}, &second); code != http.StatusOK {
		t.Fatalf("second login verify: got %d", code)
	}
	sessionB := second.AccessToken

	// Both sessions are live.
	if code := call(t, srv, http.MethodGet, "/api/v1/auth/2fa", sessionA, nil, nil); code != http.StatusOK {
		t.Fatalf("session A pre-disable: got %d", code)
	}
	if code := call(t, srv, http.MethodGet, "/api/v1/auth/2fa", sessionB, nil, nil); code != http.StatusOK {
		t.Fatalf("session B pre-disable: got %d", code)
	}

	// Disable 2FA using session B (proof = a fresh TOTP code).
	if code := call(t, srv, http.MethodDelete, "/api/v1/auth/2fa/totp", sessionB,
		map[string]string{"code": totpAt(secret, time.Now())}, nil); code != http.StatusNoContent {
		t.Fatalf("disable: got %d, want 204", code)
	}

	// Session B (the acting session) is still alive; session A (the OTHER session)
	// is revoked and busted, so it 401s promptly despite the warm cache.
	if code := call(t, srv, http.MethodGet, "/api/v1/auth/2fa", sessionB, nil, nil); code != http.StatusOK {
		t.Fatalf("acting session after disable: got %d, want 200", code)
	}
	if code := call(t, srv, http.MethodGet, "/api/v1/auth/2fa", sessionA, nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("other session after disable: got %d, want 401 (revoked+busted)", code)
	}

	// 2FA is now off: a fresh login issues a session directly (no challenge).
	var relogin sessionBody
	if code := call(t, srv, http.MethodPost, "/api/v1/auth/login", "",
		map[string]string{"email": email, "password": password}, &relogin); code != http.StatusOK {
		t.Fatalf("post-disable login: got %d", code)
	}
	if relogin.TwoFactorRequired || relogin.AccessToken == "" {
		t.Fatalf("post-disable login should issue a session directly, got %+v", relogin)
	}
}

// TestLoginEnumerationSafe proves a wrong password never reveals 2FA status: a
// 2FA user and a non-2FA user both get a flat 401 with no challenge.
func TestLoginEnumerationSafe(t *testing.T) {
	srv, _ := testServer(t, 0)
	email2fa, _, _, _, _ := enrolledUser(t, srv, "enum2fa")

	// Non-2FA user.
	emailPlain := uniqueEmail(t, "enumplain")
	if code := call(t, srv, http.MethodPost, "/api/v1/auth/register", "",
		map[string]string{"workspace_name": "Acme", "email": emailPlain, "password": "correct-horse-battery-staple-2fa"}, nil); code != http.StatusOK {
		t.Fatalf("register plain: got %d", code)
	}

	for _, email := range []string{email2fa, emailPlain} {
		var body sessionBody
		code := call(t, srv, http.MethodPost, "/api/v1/auth/login", "",
			map[string]string{"email": email, "password": "the-wrong-password"}, &body)
		if code != http.StatusUnauthorized {
			t.Fatalf("wrong password for %s: got %d, want 401", email, code)
		}
		if body.TwoFactorRequired || body.Challenge != "" {
			t.Fatalf("wrong password for %s leaked 2FA status: %+v", email, body)
		}
	}
}

// loginChallenge logs in and returns the issued challenge token (failing the test
// if login didn't gate).
func loginChallenge(t *testing.T, srv *httptest.Server, email, password string) string {
	t.Helper()
	var login sessionBody
	if code := call(t, srv, http.MethodPost, "/api/v1/auth/login", "",
		map[string]string{"email": email, "password": password}, &login); code != http.StatusOK {
		t.Fatalf("login: got %d", code)
	}
	if !login.TwoFactorRequired || login.Challenge == "" {
		t.Fatalf("expected a challenge, got %+v", login)
	}
	return login.Challenge
}
