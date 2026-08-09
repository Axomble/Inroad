package warmup

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/app/auth"
)

const testSecret = "0123456789abcdef0123456789abcdef"

// authedRouter mounts the warmup surface exactly as cmd/inroad does — the
// per-mailbox routes under the (authenticated) mailbox scope and the overview
// under /warmup — behind auth.RequireAuth, so tests exercise real routing, JWT
// claim extraction, and URL-param binding.
func authedRouter(h *Handler) http.Handler {
	r := chi.NewRouter()
	r.Use(auth.RequireAuth(auth.NewJWTVerifier([]byte(testSecret))))
	h.Register(r)
	r.Mount("/warmup", h.Routes())
	return r
}

func bearer(t *testing.T, ws uuid.UUID) string {
	t.Helper()
	tok, err := auth.IssueToken([]byte(testSecret), auth.Claims{
		UserID: uuid.New().String(), WorkspaceID: ws.String(), Role: "owner", SessionID: uuid.New().String(),
	}, time.Hour)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	return "Bearer " + tok
}

func do(t *testing.T, h http.Handler, method, target, authz, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, http.NoBody)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	if authz != "" {
		r.Header.Set("Authorization", authz)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// TestEnableBadSettingsIs400 proves boundary validation (start_volume >
// max_volume) rejects with 400 and never mutates the store.
func TestEnableBadSettingsIs400(t *testing.T) {
	ws, mb := uuid.New(), uuid.New()
	store := newFakeStore()
	store.ownedMailboxes[mb] = ws
	h := NewHandler(NewService(store))

	w := do(t, authedRouter(h), http.MethodPut, "/"+mb.String()+"/warmup", bearer(t, ws),
		`{"start_volume":50,"max_volume":40}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", w.Code, w.Body.String())
	}
	if store.upsertCalls != 0 {
		t.Fatalf("bad settings must not reach the store")
	}
}

// TestEnableNonOwnedMailboxIs404 proves a mailbox not owned by the caller's
// workspace 404s (the store's ErrMailboxNotInWorkspace mapped to ErrNotFound).
func TestEnableNonOwnedMailboxIs404(t *testing.T) {
	ws, other, mb := uuid.New(), uuid.New(), uuid.New()
	store := newFakeStore()
	store.ownedMailboxes[mb] = other // owned elsewhere
	h := NewHandler(NewService(store))

	w := do(t, authedRouter(h), http.MethodPut, "/"+mb.String()+"/warmup", bearer(t, ws), "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d: %s", w.Code, w.Body.String())
	}
}

// TestEnableUnauthenticatedIs401 proves the route fails closed without a token.
func TestEnableUnauthenticatedIs401(t *testing.T) {
	mb := uuid.New()
	h := NewHandler(NewService(newFakeStore()))
	w := do(t, authedRouter(h), http.MethodPut, "/"+mb.String()+"/warmup", "", "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
}

// TestEnableHappyPathUsesWorkspaceFromToken proves a valid enable returns 200
// with the contract fields, and that the workspace is taken from the JWT (the
// participant is written under the token's workspace, never a body/path value).
func TestEnableHappyPathUsesWorkspaceFromToken(t *testing.T) {
	ws, mb := uuid.New(), uuid.New()
	store := newFakeStore()
	store.ownedMailboxes[mb] = ws
	h := NewHandler(NewService(store))

	w := do(t, authedRouter(h), http.MethodPut, "/"+mb.String()+"/warmup", bearer(t, ws),
		`{"start_volume":6,"max_volume":50,"ramp_increment":3,"reply_rate":0.25}`)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["mailbox_id"] != mb.String() || resp["start_volume"].(float64) != 6 {
		t.Fatalf("contract fields wrong: %+v", resp)
	}
	for _, k := range []string{"enabled", "max_volume", "ramp_increment", "reply_rate",
		"health_state", "health_reason", "started_at", "today_sent", "today_target"} {
		if _, ok := resp[k]; !ok {
			t.Fatalf("contract missing key %q: %+v", k, resp)
		}
	}
	// Workspace-from-token: the store recorded the participant under ws.
	if p, ok := store.participants[mb]; !ok || p.WorkspaceID != ws {
		t.Fatalf("participant not written under token workspace: %+v", store.participants[mb])
	}
}

// TestGetDetail404ForNonParticipant proves GET on a mailbox that isn't a
// participant 404s.
func TestGetDetail404ForNonParticipant(t *testing.T) {
	ws, mb := uuid.New(), uuid.New()
	h := NewHandler(NewService(newFakeStore()))
	w := do(t, authedRouter(h), http.MethodGet, "/"+mb.String()+"/warmup", bearer(t, ws), "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d: %s", w.Code, w.Body.String())
	}
}

// TestGetDetailHappyPath proves GET returns 200 with participant + series.
func TestGetDetailHappyPath(t *testing.T) {
	ws, mb := uuid.New(), uuid.New()
	store := newFakeStore()
	store.participants[mb] = Participant{
		MailboxID: mb, WorkspaceID: ws, Enabled: true, StartVolume: 4, MaxVolume: 40,
		RampIncrement: 2, ReplyRate: 0.3, HealthState: "healthy",
		StartedAt: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
	}
	store.dailyStats[mb] = []DayStat{
		{Day: pgtype.Date{Time: time.Date(2026, 7, 25, 0, 0, 0, 0, time.UTC), Valid: true}, Sent: 5},
	}
	h := NewHandler(NewService(store))

	w := do(t, authedRouter(h), http.MethodGet, "/"+mb.String()+"/warmup", bearer(t, ws), "")
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp WarmupDetailDTO
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Participant.MailboxID != mb.String() || len(resp.Series) != 1 || resp.Series[0].Day != "2026-07-25" {
		t.Fatalf("detail payload wrong: %+v", resp)
	}
}

// TestDisableIs204 proves DELETE returns 204 and is idempotent.
func TestDisableIs204(t *testing.T) {
	ws, mb := uuid.New(), uuid.New()
	h := NewHandler(NewService(newFakeStore()))
	w := do(t, authedRouter(h), http.MethodDelete, "/"+mb.String()+"/warmup", bearer(t, ws), "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d: %s", w.Code, w.Body.String())
	}
}

// TestOverviewHappyPath proves GET /warmup/overview returns 200 with the pool
// summary and per-mailbox rows.
func TestOverviewHappyPath(t *testing.T) {
	ws := uuid.New()
	store := newFakeStore()
	store.enabledCount = 2
	store.overviewRows = []OverviewRow{{
		MailboxID: uuid.New(), Enabled: true, StartVolume: 4, MaxVolume: 40, RampIncrement: 2,
		StartedAt: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}, HealthState: "healthy",
		Email: "a@example.com", Inbox7d: 9, Spam7d: 1, TodaySent: 2,
	}}
	h := NewHandler(NewService(store))

	w := do(t, authedRouter(h), http.MethodGet, "/warmup/overview", bearer(t, ws), "")
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp WarmupOverviewDTO
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.PoolSize != 2 || !resp.Active || len(resp.Mailboxes) != 1 {
		t.Fatalf("overview payload wrong: %+v", resp)
	}
	if resp.Mailboxes[0].Email != "a@example.com" || resp.Mailboxes[0].InboxRate7d == nil || *resp.Mailboxes[0].InboxRate7d != 0.9 || resp.Mailboxes[0].PlacementSample7d != 10 {
		t.Fatalf("overview mailbox wrong: %+v", resp.Mailboxes[0])
	}
}
