package inbox_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/app/inbox"
)

var testSecret = []byte("0123456789abcdef0123456789abcdef")

// testWS is the workspace every authenticated request in this file carries.
var testWS = uuid.New()

func bearer(t *testing.T, ws uuid.UUID) string {
	t.Helper()
	tok, err := auth.IssueToken(testSecret, auth.Claims{
		UserID: uuid.New().String(), WorkspaceID: ws.String(), Role: "owner", SessionID: uuid.New().String(),
	}, time.Hour)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	return "Bearer " + tok
}

// serve runs one request through the REAL auth middleware and the domain's
// own router (mounted at /inbox, mirroring cmd/inroad), authenticated as
// testWS, so these tests exercise real routing, JWT claim extraction, and
// URL-param binding rather than calling handler methods directly.
func serve(t *testing.T, h *inbox.Handler, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	return do(t, h, method, target, body, bearer(t, testWS))
}

func do(t *testing.T, h *inbox.Handler, method, target, body, authz string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequestWithContext(context.Background(), method, target, http.NoBody)
	} else {
		r = httptest.NewRequestWithContext(context.Background(), method, target, strings.NewReader(body))
	}
	if authz != "" {
		r.Header.Set("Authorization", authz)
	}
	root := chi.NewRouter()
	root.Mount("/inbox", h.Routes())
	w := httptest.NewRecorder()
	auth.RequireAuth(auth.NewJWTVerifier(testSecret))(root).ServeHTTP(w, r)
	return w
}

// seedThread writes a thread directly into the fake store, bypassing
// RecordReply, so handler tests can set up state (including a foreign
// workspace's thread) without going through the service.
func seedThread(store *fakeStore, ws, mailbox uuid.UUID, subject, replyClass string) inbox.Thread {
	th := inbox.Thread{
		ID: uuid.New(), WorkspaceID: ws, MailboxID: mailbox, RootMessageID: uuid.NewString(),
		Subject: subject, LastReplyClass: replyClass, Unread: true, LastMessageAt: time.Now().UTC(),
	}
	store.threads[th.ID] = th
	return th
}

func TestListUnauthenticatedIs401(t *testing.T) {
	h := inbox.NewHandler(inbox.NewService(newFakeStore()))
	w := do(t, h, http.MethodGet, "/inbox/threads", "", "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d: %s", w.Code, w.Body.String())
	}
}

func TestListHappyPathReturnsWorkspaceThreads(t *testing.T) {
	store := newFakeStore()
	th := seedThread(store, testWS, uuid.New(), "Re: Hi", "neutral")
	h := inbox.NewHandler(inbox.NewService(store))

	w := serve(t, h, http.MethodGet, "/inbox/threads", "")
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Items []struct {
			ID      string `json:"id"`
			Subject string `json:"subject"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Items) != 1 || resp.Items[0].ID != th.ID.String() || resp.Items[0].Subject != "Re: Hi" {
		t.Fatalf("items = %+v, want exactly the seeded thread", resp.Items)
	}
}

// A caller must never see another workspace's thread, even though both live
// in the same fake store — proving the handler passes the JWT's workspace
// through rather than trusting anything in the request.
func TestListNeverReturnsAnotherWorkspacesThreads(t *testing.T) {
	store := newFakeStore()
	seedThread(store, uuid.New(), uuid.New(), "Foreign", "neutral")
	h := inbox.NewHandler(inbox.NewService(store))

	w := serve(t, h, http.MethodGet, "/inbox/threads", "")
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Items []any `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Items) != 0 {
		t.Fatalf("items = %+v, want none (all seeded threads belong to another workspace)", resp.Items)
	}
}

func TestListBadLimitIs400(t *testing.T) {
	h := inbox.NewHandler(inbox.NewService(newFakeStore()))
	w := serve(t, h, http.MethodGet, "/inbox/threads?limit=notanumber", "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestListHalfSetCursorIs400(t *testing.T) {
	h := inbox.NewHandler(inbox.NewService(newFakeStore()))
	w := serve(t, h, http.MethodGet, "/inbox/threads?before_id="+uuid.NewString(), "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGetThreadNotFoundIs404(t *testing.T) {
	h := inbox.NewHandler(inbox.NewService(newFakeStore()))
	w := serve(t, h, http.MethodGet, "/inbox/threads/"+uuid.NewString(), "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGetThreadHappyPathIncludesMessages(t *testing.T) {
	store := newFakeStore()
	th := seedThread(store, testWS, uuid.New(), "Re: Hi", "positive")
	store.messages[th.ID] = []inbox.Message{{
		ThreadID: th.ID, Direction: "inbound", FromEmail: "them@example.com",
		BodyText: "sounds good", OccurredAt: time.Now().UTC(),
	}}
	h := inbox.NewHandler(inbox.NewService(store))

	w := serve(t, h, http.MethodGet, "/inbox/threads/"+th.ID.String(), "")
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		ID       string `json:"id"`
		Messages []struct {
			FromEmail string `json:"from_email"`
			BodyText  string `json:"body_text"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ID != th.ID.String() || len(resp.Messages) != 1 || resp.Messages[0].FromEmail != "them@example.com" {
		t.Fatalf("detail = %+v, want the seeded thread and its one message", resp)
	}
}

func TestSetReadTogglesUnreadAnd204s(t *testing.T) {
	store := newFakeStore()
	th := seedThread(store, testWS, uuid.New(), "Re: Hi", "neutral")
	h := inbox.NewHandler(inbox.NewService(store))

	w := serve(t, h, http.MethodPut, "/inbox/threads/"+th.ID.String()+"/read", `{"unread":false}`)
	if w.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d: %s", w.Code, w.Body.String())
	}
	if store.threads[th.ID].Unread {
		t.Fatal("thread still unread in the store after PUT read")
	}
}

func TestSetReadOnForeignThreadIs404(t *testing.T) {
	store := newFakeStore()
	th := seedThread(store, uuid.New(), uuid.New(), "Foreign", "neutral")
	h := inbox.NewHandler(inbox.NewService(store))

	w := serve(t, h, http.MethodPut, "/inbox/threads/"+th.ID.String()+"/read", `{"unread":false}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d: %s", w.Code, w.Body.String())
	}
	if !store.threads[th.ID].Unread {
		t.Fatal("a cross-tenant PUT read mutated another workspace's thread")
	}
}

func TestSetReadInvalidJSONIs400(t *testing.T) {
	store := newFakeStore()
	th := seedThread(store, testWS, uuid.New(), "Re: Hi", "neutral")
	h := inbox.NewHandler(inbox.NewService(store))

	w := serve(t, h, http.MethodPut, "/inbox/threads/"+th.ID.String()+"/read", `not json`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", w.Code, w.Body.String())
	}
}
