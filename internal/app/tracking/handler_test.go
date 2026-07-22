package tracking

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/track"
)

const testUA = "test-agent/1.0"

var testSecret = []byte("0123456789abcdef0123456789abcdef")

type sendTenant struct{ workspaceID, campaignID uuid.UUID }

type recordCall struct {
	workspaceID, campaignID, sendID uuid.UUID
	kind, url, userAgent            string
}

// fakeStore is a no-database double for Store: sends holds the fixture of
// sends that "exist" (ResolveSend succeeds only for keys present here), and
// calls records every RecordEvent invocation so tests can assert exactly
// what (and how often) was recorded.
type fakeStore struct {
	sends map[uuid.UUID]sendTenant
	calls []recordCall
}

func (f *fakeStore) RecordEvent(_ context.Context, workspaceID, campaignID, sendID uuid.UUID, kind, url, userAgent string) error {
	f.calls = append(f.calls, recordCall{workspaceID, campaignID, sendID, kind, url, userAgent})
	return nil
}

func (f *fakeStore) ResolveSend(_ context.Context, sendID uuid.UUID) (uuid.UUID, uuid.UUID, bool) {
	t, ok := f.sends[sendID]
	if !ok {
		return uuid.Nil, uuid.Nil, false
	}
	return t.workspaceID, t.campaignID, true
}

// newTestHandler wires a Handler over a fakeStore seeded with one known
// send, mounted the same way cmd/inroad mounts it in production (under /t).
func newTestHandler(t *testing.T) (http.Handler, *fakeStore, uuid.UUID) {
	t.Helper()
	sendID := uuid.New()
	store := &fakeStore{sends: map[uuid.UUID]sendTenant{
		sendID: {workspaceID: uuid.New(), campaignID: uuid.New()},
	}}
	h := NewHandler(NewService(testSecret, store))
	r := chi.NewRouter()
	r.Mount("/t", h.Routes())
	return r, store, sendID
}

func TestOpenGIF_ValidToken_RecordsEventAndServesPixel(t *testing.T) {
	r, store, sendID := newTestHandler(t)
	tok := track.MakeOpenToken(testSecret, sendID.String())

	req := httptest.NewRequest(http.MethodGet, "/t/o/"+tok+".gif", nil)
	req.Header.Set("User-Agent", testUA)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "image/gif" {
		t.Errorf("Content-Type = %q, want image/gif", ct)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	if w.Body.Len() == 0 {
		t.Error("expected a non-empty pixel body")
	}
	if len(store.calls) != 1 {
		t.Fatalf("recorded %d events, want 1", len(store.calls))
	}
	c := store.calls[0]
	if c.kind != "open" || c.sendID != sendID || c.userAgent != testUA || c.url != "" {
		t.Errorf("recorded event = %+v, want kind=open sendID=%s ua=%s url=\"\"", c, sendID, testUA)
	}
}

func TestOpenGIF_InvalidToken_ServesPixelButRecordsNothing(t *testing.T) {
	r, store, _ := newTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/t/o/not-a-real-token.gif", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (the pixel must never fail, even for a bad token)", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "image/gif" {
		t.Errorf("Content-Type = %q, want image/gif", ct)
	}
	if len(store.calls) != 0 {
		t.Fatalf("recorded %d events for an invalid token, want 0", len(store.calls))
	}
}

func TestOpenGIF_UnknownSend_ServesPixelButRecordsNothing(t *testing.T) {
	r, store, _ := newTestHandler(t)
	tok := track.MakeOpenToken(testSecret, uuid.New().String()) // validly signed, no such send

	req := httptest.NewRequest(http.MethodGet, "/t/o/"+tok+".gif", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if len(store.calls) != 0 {
		t.Fatalf("recorded %d events for an unknown send, want 0", len(store.calls))
	}
}

func TestClickRedirect_ValidToken_RecordsEventAndRedirects(t *testing.T) {
	r, store, sendID := newTestHandler(t)
	dest := "https://example.test/landing?utm_source=inroad"
	tok := track.MakeClickToken(testSecret, sendID.String(), dest)

	req := httptest.NewRequest(http.MethodGet, "/t/c/"+tok, nil)
	req.Header.Set("User-Agent", testUA)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != dest {
		t.Fatalf("Location = %q, want %q", loc, dest)
	}
	if len(store.calls) != 1 {
		t.Fatalf("recorded %d events, want 1", len(store.calls))
	}
	c := store.calls[0]
	if c.kind != "click" || c.sendID != sendID || c.url != dest || c.userAgent != testUA {
		t.Errorf("recorded event = %+v, want kind=click sendID=%s url=%s ua=%s", c, sendID, dest, testUA)
	}
}

func TestClickRedirect_TamperedToken_404NoRedirectNoEvent(t *testing.T) {
	r, store, sendID := newTestHandler(t)
	tok := track.MakeClickToken(testSecret, sendID.String(), "https://example.test/landing")
	tampered := tok[:len(tok)-1] + "x"
	if tampered == tok {
		tampered = tok[:len(tok)-1] + "y"
	}

	req := httptest.NewRequest(http.MethodGet, "/t/c/"+tampered, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "" {
		t.Fatalf("Location = %q, want no redirect", loc)
	}
	if len(store.calls) != 0 {
		t.Fatalf("recorded %d events for a tampered token, want 0", len(store.calls))
	}
}

// TestClickRedirect_UnsafeScheme_404NoRedirectNoEvent covers the token
// integrity vs. URL safety distinction: the HMAC proves the payload wasn't
// altered, but says nothing about whether the URL it names was ever safe to
// redirect to. A javascript: URL, however it was signed, must never 302.
func TestClickRedirect_UnsafeScheme_404NoRedirectNoEvent(t *testing.T) {
	r, store, sendID := newTestHandler(t)
	tok := track.MakeClickToken(testSecret, sendID.String(), "javascript:alert(1)")

	req := httptest.NewRequest(http.MethodGet, "/t/c/"+tok, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "" {
		t.Fatalf("Location = %q, want no redirect", loc)
	}
	if len(store.calls) != 0 {
		t.Fatalf("recorded %d events for an unsafe scheme, want 0", len(store.calls))
	}
}

func TestClickRedirect_UnknownSend_404NoRedirectNoEvent(t *testing.T) {
	r, store, _ := newTestHandler(t)
	tok := track.MakeClickToken(testSecret, uuid.New().String(), "https://example.test/landing")

	req := httptest.NewRequest(http.MethodGet, "/t/c/"+tok, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	if len(store.calls) != 0 {
		t.Fatalf("recorded %d events for an unknown send, want 0", len(store.calls))
	}
}
