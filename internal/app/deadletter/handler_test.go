package deadletter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

const testSecret = "0123456789abcdef0123456789abcdef"

// authedRouter mounts this domain exactly as cmd/inroad does — Routes() behind
// auth.RequireAuth — so the tests exercise real routing, JWT claim extraction,
// AND the per-route scope gates rather than bypassing them.
func authedRouter(h *Handler) http.Handler {
	r := chi.NewRouter()
	r.Use(auth.RequireAuth(auth.NewJWTVerifier([]byte(testSecret))))
	r.Mount("/dead-letters", h.Routes())
	return r
}

// bearer mints a SESSION token for ws. A session principal implicitly holds
// every scope (auth.Principal.HasScope), which is what a logged-in operator is;
// the scope gates themselves are asserted separately in routes_test.go.
func bearer(t *testing.T, ws uuid.UUID) string {
	t.Helper()
	tok, err := auth.IssueToken([]byte(testSecret), auth.Claims{
		UserID: uuid.New().String(), WorkspaceID: ws.String(),
		Role: "admin", SessionID: uuid.New().String(),
	}, time.Hour)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	return "Bearer " + tok
}

// request drives one call through the mounted router as the workspace ws.
func request(t *testing.T, h *Handler, method, path string, ws uuid.UUID) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequestWithContext(context.Background(), method, "/dead-letters"+path, http.NoBody)
	r.Header.Set("Authorization", bearer(t, ws))
	w := httptest.NewRecorder()
	authedRouter(h).ServeHTTP(w, r)
	return w
}

func handlerFixture(t *testing.T) (*fakeStore, *fakeEnqueuer, *Handler, uuid.UUID, gen.TaskDeadLetter) {
	t.Helper()
	store, enq, svc, ws, row := seedPending(t)
	return store, enq, NewHandler(svc), ws, row
}

// A replay of a pending row is 200 and reports the row as replayed.
func TestHandlerReplayReturns200(t *testing.T) {
	_, enq, h, ws, row := handlerFixture(t)

	w := request(t, h, http.MethodPost, "/"+row.ID.String()+"/replay", ws)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body)
	}
	var got deadLetterResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Status != StatusReplayed {
		t.Errorf("status = %q, want %q", got.Status, StatusReplayed)
	}
	if got.ReplayedAt == nil {
		t.Error("replayed_at is null on a replayed row")
	}
	if enq.count() != 1 {
		t.Errorf("enqueued %d times, want 1", enq.count())
	}
}

// Replaying twice is 409, not a second enqueue — the double-send guard as the
// client sees it.
func TestHandlerReplayTwiceIs409(t *testing.T) {
	_, enq, h, ws, row := handlerFixture(t)

	if w := request(t, h, http.MethodPost, "/"+row.ID.String()+"/replay", ws); w.Code != http.StatusOK {
		t.Fatalf("first replay status = %d, want 200", w.Code)
	}
	w := request(t, h, http.MethodPost, "/"+row.ID.String()+"/replay", ws)
	if w.Code != http.StatusConflict {
		t.Fatalf("second replay status = %d, want 409: %s", w.Code, w.Body)
	}
	if enq.count() != 1 {
		t.Errorf("enqueued %d times across two requests, want 1", enq.count())
	}
}

// A malformed payload is 422 (permanent, do not retry), never 500.
func TestHandlerReplayOfAMalformedPayloadIs422(t *testing.T) {
	store, enq, svc, ws, _ := seedPending(t)
	h := NewHandler(svc)
	bad := store.seed(gen.TaskDeadLetter{
		WorkspaceID: ws, TaskType: "sequence:advance",
		Payload: []byte(`{"no":"workspace"}`), Status: StatusPending,
	})

	w := request(t, h, http.MethodPost, "/"+bad.ID.String()+"/replay", ws)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", w.Code, w.Body)
	}
	if enq.count() != 0 {
		t.Errorf("enqueued %d times, want 0", enq.count())
	}
}

// Another tenant's row, and an unparseable id, are both 404 — indistinguishable,
// so the endpoint cannot be used to probe for foreign ids.
func TestHandlerReturns404ForForeignAndMalformedIDs(t *testing.T) {
	_, _, h, _, row := handlerFixture(t)
	intruder := uuid.New()

	for _, tc := range []struct {
		name   string
		target string
		ws     uuid.UUID
	}{
		{"another tenant's row", "/" + row.ID.String() + "/replay", intruder},
		{"an id that is not a UUID", "/not-a-uuid/replay", intruder},
		{"an unknown id", "/" + uuid.NewString() + "/replay", intruder},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if w := request(t, h, http.MethodPost, tc.target, tc.ws); w.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404: %s", w.Code, w.Body)
			}
		})
	}
}

// Discard is 204 and terminal.
func TestHandlerDiscard(t *testing.T) {
	store, _, h, ws, row := handlerFixture(t)

	if w := request(t, h, http.MethodPost, "/"+row.ID.String()+"/discard", ws); w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}
	if store.status(row.ID) != StatusDiscarded {
		t.Errorf("status = %q, want %q", store.status(row.ID), StatusDiscarded)
	}
	if w := request(t, h, http.MethodPost, "/"+row.ID.String()+"/discard", ws); w.Code != http.StatusConflict {
		t.Errorf("second discard status = %d, want 409", w.Code)
	}
}

// The list response never echoes a tenant id back, and an empty list is [] not
// null so a client can map over it unguarded.
func TestHandlerListShape(t *testing.T) {
	_, _, h, ws, _ := handlerFixture(t)

	w := request(t, h, http.MethodGet, "/", ws)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body)
	}
	var envelope struct {
		DeadLetters []map[string]json.RawMessage `json:"dead_letters"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(envelope.DeadLetters) != 1 {
		t.Fatalf("returned %d rows, want 1", len(envelope.DeadLetters))
	}
	if _, leaked := envelope.DeadLetters[0]["workspace_id"]; leaked {
		t.Error("the response echoes workspace_id back to the caller")
	}
	// payload is raw JSON, not a string, so a client reads its fields directly.
	var payload map[string]string
	if err := json.Unmarshal(envelope.DeadLetters[0]["payload"], &payload); err != nil {
		t.Fatalf("payload is not raw JSON: %v", err)
	}

	empty := request(t, h, http.MethodGet, "/", uuid.New())
	if empty.Code != http.StatusOK {
		t.Fatalf("empty list status = %d, want 200", empty.Code)
	}
	var emptyEnvelope struct {
		DeadLetters []deadLetterResponse `json:"dead_letters"`
	}
	if err := json.Unmarshal(empty.Body.Bytes(), &emptyEnvelope); err != nil {
		t.Fatalf("decode empty: %v", err)
	}
	if emptyEnvelope.DeadLetters == nil {
		t.Error("an empty list serialised as null, want []")
	}
}

// An unknown status filter is a 422 rather than a silently empty list.
func TestHandlerListRejectsAnUnknownStatusFilter(t *testing.T) {
	_, _, h, ws, _ := handlerFixture(t)

	if w := request(t, h, http.MethodGet, "/?status=exploded", ws); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422: %s", w.Code, w.Body)
	}
	for _, status := range []string{StatusPending, StatusReplayed, StatusDiscarded} {
		if w := request(t, h, http.MethodGet, "/?status="+status, ws); w.Code != http.StatusOK {
			t.Errorf("status=%s -> %d, want 200", status, w.Code)
		}
	}
}

// A garbage limit/offset falls back to the default page rather than erroring —
// paging is a convenience, not a contract the client can break.
func TestHandlerTolueratesGarbagePaging(t *testing.T) {
	_, _, h, ws, _ := handlerFixture(t)

	for _, q := range []string{"?limit=abc", "?limit=-1", "?offset=abc", "?limit=99999999999999999999"} {
		if w := request(t, h, http.MethodGet, "/"+q, ws); w.Code != http.StatusOK {
			t.Errorf("GET /%s -> %d, want 200: %s", q, w.Code, w.Body)
		}
	}
}

// A store failure is a 500, and must not be reported as a client error.
func TestHandlerStoreFailureIs500(t *testing.T) {
	store, _, h, ws, _ := handlerFixture(t)
	store.listErr = context.DeadlineExceeded

	if w := request(t, h, http.MethodGet, "/", ws); w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500: %s", w.Code, w.Body)
	}
}
