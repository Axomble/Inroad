//go:build integration

package twofa

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/app/identity"
	"github.com/inroad/inroad/internal/platform/crypto"
	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/notify"
)

// nextStepCode returns a TOTP code for the step AFTER the current one. Confirm (and
// any prior login) advances the per-user replay high-water mark to the step it
// matched, so a fresh login must present a strictly-later step. A now+period code
// is exactly +1 step: still inside the ±1 verification skew, yet always past the
// confirm step — deterministic without sleeping across a real boundary.
func nextStepCode(secret []byte) string {
	return totpAt(secret, time.Now().Add(totpPeriodSec*time.Second))
}

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
func testServer(t *testing.T, cacheTTL time.Duration) (*httptest.Server, *gen.Queries, *Service) {
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
	r.Mount("/api/v1/auth", identHandler.Routes(verifier, twofaHandler.Routes(verifier), nil, nil))
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv, gen.New(pool), twofaSvc
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
	srv, _, _ := testServer(t, 0)
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

	// Verify with a next-step TOTP code issues a real session (confirm consumed the
	// current step, so a fresh login uses the following step).
	var verified sessionBody
	if code := call(t, srv, http.MethodPost, "/api/v1/auth/2fa/verify", "",
		map[string]string{"challenge": login.Challenge, "code": nextStepCode(secret)}, &verified); code != http.StatusOK {
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
		map[string]string{"challenge": login.Challenge, "code": nextStepCode(secret)}, &replay); code != http.StatusUnauthorized {
		t.Fatalf("challenge replay: got %d, want 401", code)
	}
}

// TestLoginVerifyWithRecoveryCode proves a recovery code satisfies the gate and
// is single-use.
func TestLoginVerifyWithRecoveryCode(t *testing.T) {
	srv, _, _ := testServer(t, 0)
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
	srv, _, _ := testServer(t, 0)
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
	srv, _, _ := testServer(t, time.Hour) // long TTL: only an explicit Bust kills a token promptly
	email, password, sessionA, secret, recovery := enrolledUser(t, srv, "disable")

	// Establish a SECOND session via the 2FA login flow. Use a recovery code here so
	// it doesn't consume a TOTP step — leaving the following TOTP step free for the
	// disable proof below (the replay high-water mark forbids reusing a step).
	challenge := loginChallenge(t, srv, email, password)
	var second sessionBody
	if code := call(t, srv, http.MethodPost, "/api/v1/auth/2fa/verify", "",
		map[string]string{"challenge": challenge, "code": recovery[0]}, &second); code != http.StatusOK {
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

	// Disable 2FA using session B (proof = a fresh, next-step TOTP code).
	if code := call(t, srv, http.MethodDelete, "/api/v1/auth/2fa/totp", sessionB,
		map[string]string{"code": nextStepCode(secret)}, nil); code != http.StatusNoContent {
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
	srv, _, _ := testServer(t, 0)
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

// TestTOTPStepReplayRejected proves FIX 1 over the real DB path: a TOTP code
// accepted for one login challenge cannot satisfy a SECOND challenge within its
// validity window (its step is now consumed), while a strictly-later step still
// works. The service clock is driven deterministically so codes and verification
// share the same time-step without sleeping across a real boundary.
func TestTOTPStepReplayRejected(t *testing.T) {
	srv, _, svc := testServer(t, 0)
	email, password, _, secret, _ := enrolledUser(t, srv, "replay")

	// Confirm consumed the enrollment step; drive the clock two steps forward and
	// operate there so every code below is strictly past that mark.
	base := time.Now().Add(2 * totpPeriodSec * time.Second)
	svc.now = func() time.Time { return base }
	code := totpAt(secret, base)

	// First login with the code succeeds and consumes its step.
	ch1 := loginChallenge(t, srv, email, password)
	if got := call(t, srv, http.MethodPost, "/api/v1/auth/2fa/verify", "",
		map[string]string{"challenge": ch1, "code": code}, nil); got != http.StatusOK {
		t.Fatalf("first verify: got %d, want 200", got)
	}

	// Second, DIFFERENT challenge with the SAME still-valid code: rejected — the
	// step is now <= last_step, so the code can't establish a second session.
	ch2 := loginChallenge(t, srv, email, password)
	if got := call(t, srv, http.MethodPost, "/api/v1/auth/2fa/verify", "",
		map[string]string{"challenge": ch2, "code": code}, nil); got != http.StatusUnauthorized {
		t.Fatalf("replayed step: got %d, want 401", got)
	}

	// A strictly-later step still works: advance the clock one step and use it.
	svc.now = func() time.Time { return base.Add(totpPeriodSec * time.Second) }
	ch3 := loginChallenge(t, srv, email, password)
	if got := call(t, srv, http.MethodPost, "/api/v1/auth/2fa/verify", "",
		map[string]string{"challenge": ch3, "code": totpAt(secret, base.Add(totpPeriodSec*time.Second))}, nil); got != http.StatusOK {
		t.Fatalf("next-step verify: got %d, want 200", got)
	}
}

// TestConcurrentAttemptCapNeverExceeds fires N parallel wrong-code verifies at one
// challenge and proves the attempt cap held ATOMICALLY: attempts never exceeds
// maxChallengeAttempts (a read-then-increment would have let all N through), no
// wrong code was accepted, and the challenge is dead afterward. Mirrors identity's
// concurrent-reuse race test.
func TestConcurrentAttemptCapNeverExceeds(t *testing.T) {
	srv, q, _ := testServer(t, 0)
	email, password, _, secret, _ := enrolledUser(t, srv, "conccap")
	challenge := loginChallenge(t, srv, email, password)

	const workers = 20
	var wg sync.WaitGroup
	var accepted int32
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if code := call(t, srv, http.MethodPost, "/api/v1/auth/2fa/verify", "",
				map[string]string{"challenge": challenge, "code": "000000"}, nil); code == http.StatusOK {
				atomic.AddInt32(&accepted, 1)
			}
		}()
	}
	wg.Wait()

	if accepted != 0 {
		t.Fatalf("a wrong code was accepted %d times", accepted)
	}
	ch, err := q.GetChallengeByHash(context.Background(), auth.HashToken(challenge))
	if err != nil {
		t.Fatalf("get challenge: %v", err)
	}
	if ch.Attempts != maxChallengeAttempts {
		t.Fatalf("attempts = %d, want exactly the cap %d (atomic claim neither overshot nor undershot)", ch.Attempts, maxChallengeAttempts)
	}
	// Dead challenge: even a correct, fresh-step code is rejected.
	if code := call(t, srv, http.MethodPost, "/api/v1/auth/2fa/verify", "",
		map[string]string{"challenge": challenge, "code": nextStepCode(secret)}, nil); code != http.StatusUnauthorized {
		t.Fatalf("correct code after concurrent cap: got %d, want 401", code)
	}
}

// TestRecoveryCodeExhaustionAPI drives all 10 recovery codes to used over the real
// login path and proves recovery_codes_remaining reaches 0 and a spent code no
// longer verifies. Recovery codes don't touch the TOTP replay mark, so each login
// only needs its own fresh challenge.
func TestRecoveryCodeExhaustionAPI(t *testing.T) {
	srv, _, _ := testServer(t, 0)
	email, password, access, _, recovery := enrolledUser(t, srv, "recexh")

	for i, rc := range recovery {
		ch := loginChallenge(t, srv, email, password)
		if code := call(t, srv, http.MethodPost, "/api/v1/auth/2fa/verify", "",
			map[string]string{"challenge": ch, "code": rc}, nil); code != http.StatusOK {
			t.Fatalf("verify recovery %d: got %d, want 200", i, code)
		}
	}

	var st statusResponse
	if code := call(t, srv, http.MethodGet, "/api/v1/auth/2fa", access, nil, &st); code != http.StatusOK {
		t.Fatalf("status: got %d", code)
	}
	if st.RecoveryCodesRemaining != 0 {
		t.Fatalf("recovery_codes_remaining = %d, want 0 after exhausting all %d", st.RecoveryCodesRemaining, recoveryCodeCount)
	}

	// A spent code no longer verifies.
	ch := loginChallenge(t, srv, email, password)
	if code := call(t, srv, http.MethodPost, "/api/v1/auth/2fa/verify", "",
		map[string]string{"challenge": ch, "code": recovery[0]}, nil); code != http.StatusUnauthorized {
		t.Fatalf("spent recovery code: got %d, want 401", code)
	}
}

// TestNullIPThrottleCountsTogether proves FIX 3 at the SQL level: challenges from
// an unknown (NULL) IP share ONE bucket via IS NOT DISTINCT FROM, so the per-IP
// throttle counts them together (fail closed) instead of matching nothing, while a
// real IP stays its own bucket.
func TestNullIPThrottleCountsTogether(t *testing.T) {
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

	// A real user id (challenges FK-reference users).
	var uid uuid.UUID
	if err := pool.QueryRow(ctx,
		"INSERT INTO users (email, password_hash) VALUES ($1, $2) RETURNING id",
		uniqueEmail(t, "nullip"), "x").Scan(&uid); err != nil {
		t.Fatalf("insert user: %v", err)
	}

	store := NewPgStore(pool)
	since := time.Now().Add(-time.Hour)
	exp := time.Now().Add(time.Hour)

	const nNull, nIP = 3, 2
	for i := 0; i < nNull; i++ {
		if _, err := store.CreateChallenge(ctx, uid, []byte(fmt.Sprintf("null-%d", i)), nil, exp); err != nil {
			t.Fatalf("create null-ip challenge %d: %v", i, err)
		}
	}
	ip := netip.MustParseAddr("203.0.113.9")
	for i := 0; i < nIP; i++ {
		if _, err := store.CreateChallenge(ctx, uid, []byte(fmt.Sprintf("ip-%d", i)), &ip, exp); err != nil {
			t.Fatalf("create ip challenge %d: %v", i, err)
		}
	}

	// Unknown-IP callers share one bucket — all NULLs counted together.
	if n, err := store.CountRecentChallengesForIP(ctx, nil, since); err != nil || n != nNull {
		t.Fatalf("null-ip count = %d (err %v), want %d (nulls bucketed together)", n, err, nNull)
	}
	// A real IP is its own bucket, not conflated with the NULLs.
	if n, err := store.CountRecentChallengesForIP(ctx, &ip, since); err != nil || n != nIP {
		t.Fatalf("real-ip count = %d (err %v), want %d", n, err, nIP)
	}
}
