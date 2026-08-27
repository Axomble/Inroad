package emailotp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
)

// fakeCompleter records the first-factor completion hand-off so a test can prove
// verify delegates to it (rather than minting a session directly) — the delegation
// is what routes an OTP login through the shared 2FA gate.
type fakeCompleter struct {
	called bool
	uid    uuid.UUID
}

func (f *fakeCompleter) CompleteFirstFactor(w http.ResponseWriter, _ *http.Request, userID uuid.UUID) {
	f.called = true
	f.uid = userID
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"completed": "true"})
}

func newTestHandler(t *testing.T) (*Handler, *fakeStore, *captureSender, *fakeCompleter) {
	t.Helper()
	store := newFakeStore()
	sender := &captureSender{}
	svc := newTestService(store, sender, time.Now())
	comp := &fakeCompleter{}
	return NewHandler(svc, comp), store, sender, comp
}

func post(t *testing.T, h *Handler, path string, body any) *http.Response {
	t.Helper()
	buf, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, path, bytes.NewReader(buf))
	rec := httptest.NewRecorder()
	h.Routes(nil, nil).ServeHTTP(rec, req)
	return rec.Result()
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	_ = resp.Body.Close()
	return string(b)
}

// TestStartIsAntiEnumeration asserts start returns the SAME status and body for a
// registered and an unregistered email — no oracle distinguishing the two.
func TestStartIsAntiEnumeration(t *testing.T) {
	h, store, _, _ := newTestHandler(t)
	store.addUser("real@example.test")

	respReal := post(t, h, "/start", map[string]string{"email": "real@example.test"})
	respGhost := post(t, h, "/start", map[string]string{"email": "ghost@example.test"})

	if respReal.StatusCode != http.StatusOK || respGhost.StatusCode != http.StatusOK {
		t.Fatalf("status: real=%d ghost=%d, want both 200", respReal.StatusCode, respGhost.StatusCode)
	}
	bodyReal := readBody(t, respReal)
	bodyGhost := readBody(t, respGhost)
	if bodyReal != bodyGhost {
		t.Fatalf("body differs (enumeration oracle): real=%q ghost=%q", bodyReal, bodyGhost)
	}
}

// TestStartInvalidEmailRejected asserts a malformed email is a 400 (validation),
// distinct from the anti-enumeration 200 for a well-formed unknown address.
func TestStartInvalidEmailRejected(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	resp := post(t, h, "/start", map[string]string{"email": "not-an-email"})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed email: got %d, want 400", resp.StatusCode)
	}
}

// TestVerifySuccessDelegatesToCompleter proves a correct code hands off to the
// FirstFactorCompleter with the verified user id — the seam that runs the SAME 2FA
// gate a password login does, so a confirmed-2FA user is still challenged (email
// possession alone never mints a session).
func TestVerifySuccessDelegatesToCompleter(t *testing.T) {
	h, store, sender, comp := newTestHandler(t)
	uid := store.addUser("real@example.test")
	_ = h.svc.Start(context.Background(), "real@example.test")
	code := sentCode(t, sender)

	resp := post(t, h, "/verify", map[string]string{"email": "real@example.test", "code": code})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("verify: got %d, want 200", resp.StatusCode)
	}
	if !comp.called {
		t.Fatal("verify did not delegate to CompleteFirstFactor (would bypass the 2FA gate)")
	}
	if comp.uid != uid {
		t.Fatalf("completer uid: got %s, want %s", comp.uid, uid)
	}
}

// TestVerifyWrongCodeIsFlat401 asserts a wrong code is a flat 401 and never
// reaches the completer.
func TestVerifyWrongCodeIsFlat401(t *testing.T) {
	h, store, sender, comp := newTestHandler(t)
	store.addUser("real@example.test")
	_ = h.svc.Start(context.Background(), "real@example.test")
	realCode := sentCode(t, sender)
	wrong := "000000"
	if wrong == realCode {
		wrong = "111111"
	}

	resp := post(t, h, "/verify", map[string]string{"email": "real@example.test", "code": wrong})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong code: got %d, want 401", resp.StatusCode)
	}
	if comp.called {
		t.Fatal("wrong code reached the completer")
	}
}
