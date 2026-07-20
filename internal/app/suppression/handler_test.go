package suppression

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/unsub"
)

type fakeAdder struct {
	calls []addCall
	err   error
}

type addCall struct {
	workspaceID uuid.UUID
	email       string
	reason      string
}

func (f *fakeAdder) Add(_ context.Context, workspaceID uuid.UUID, email, reason string) error {
	f.calls = append(f.calls, addCall{workspaceID, email, reason})
	return f.err
}

func newTestHandler(t *testing.T) (http.Handler, *fakeAdder, []byte, uuid.UUID) {
	t.Helper()
	secret := []byte("0123456789abcdef0123456789abcdef")
	store := &fakeAdder{}
	h := NewHandler(secret, store)
	r := chi.NewRouter()
	r.Mount("/u", h.Routes())
	return r, store, secret, uuid.New()
}

// TestUnsubscribeGETDoesNotMutate is the RFC 8058 guardrail: email preview
// scanners auto-follow GETs, and if GET inserted a suppression row those
// scans would silently unsubscribe every recipient. GET only renders the
// confirmation page.
func TestUnsubscribeGETDoesNotMutate(t *testing.T) {
	r, store, secret, ws := newTestHandler(t)
	tok := unsub.MakeToken(secret, ws.String(), "alice@x.test")

	req := httptest.NewRequest(http.MethodGet, "/u/"+tok, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET: want 200, got %d", w.Code)
	}
	if len(store.calls) != 0 {
		t.Fatalf("GET must not insert; got %d calls", len(store.calls))
	}
	if ct := w.Header().Get("Content-Type"); ct == "" {
		t.Fatalf("GET missing Content-Type")
	}
}

// TestUnsubscribePOSTInserts confirms the state change lives on POST, and
// the token's decoded workspace + email make it into the store call
// verbatim (no re-parsing, no lowercasing).
func TestUnsubscribePOSTInserts(t *testing.T) {
	r, store, secret, ws := newTestHandler(t)
	tok := unsub.MakeToken(secret, ws.String(), "alice@x.test")

	req := httptest.NewRequest(http.MethodPost, "/u/"+tok, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("POST: want 200, got %d", w.Code)
	}
	if len(store.calls) != 1 {
		t.Fatalf("POST must insert once; got %d calls", len(store.calls))
	}
	c := store.calls[0]
	if c.workspaceID != ws || c.email != "alice@x.test" || c.reason != "unsubscribe" {
		t.Fatalf("POST inserted %+v; want ws=%s email=alice@x.test reason=unsubscribe", c, ws)
	}
}

// TestUnsubscribeMalformedToken400 covers both verbs: a bad token yields
// 400 (not 500, not 200) and never touches the store.
func TestUnsubscribeMalformedToken400(t *testing.T) {
	cases := []struct{ method, path string }{
		{http.MethodGet, "/u/not-a-real-token"},
		{http.MethodPost, "/u/not-a-real-token"},
		{http.MethodGet, "/u/eyJhIjoxfQ.deadbeef"}, // bad HMAC
		{http.MethodPost, "/u/eyJhIjoxfQ.deadbeef"},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			r, store, _, _ := newTestHandler(t)
			req := httptest.NewRequest(tc.method, tc.path, nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("want 400, got %d", w.Code)
			}
			if len(store.calls) != 0 {
				t.Fatalf("store must not be touched for a malformed token; got %d calls", len(store.calls))
			}
		})
	}
}
