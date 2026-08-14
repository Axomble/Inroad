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
		Lane: "watch", LaneReason: "held on watch pending clean evidence",
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

// The schema has REQUIRED lane/lane_reason on WarmupMailbox since lanes shipped,
// but the query never selected them and the DTO never carried them — so the field
// was absent from the JSON, arrived as undefined in the SPA, and every participant
// rendered the "probation" badge whatever its real lane was. A required field that
// is silently missing is worse than a wrong one: the client's safe fallback hides it.
//
// Asserted on the RAW JSON, not the decoded struct: decoding into WarmupMailboxDTO
// would happily produce "" for an absent key and the test would pass over the bug.
func TestOverviewCarriesTheLaneAxis(t *testing.T) {
	ws := uuid.New()
	store := newFakeStore()
	store.enabledCount = 2
	store.overviewRows = []OverviewRow{{
		MailboxID: uuid.New(), Enabled: true, StartVolume: 4, MaxVolume: 40, RampIncrement: 2,
		StartedAt:   pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
		HealthState: "healthy", HealthReason: "",
		Lane: "quarantine", LaneReason: "quarantined: campaign hard-bounce rate above the pause threshold",
		Email: "a@example.com", Inbox7d: 9, Spam7d: 1, TodaySent: 2,
	}}
	h := NewHandler(NewService(store))

	w := do(t, authedRouter(h), http.MethodGet, "/warmup/overview", bearer(t, ws), "")
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	var raw struct {
		Mailboxes []map[string]any `json:"mailboxes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(raw.Mailboxes) != 1 {
		t.Fatalf("want one mailbox, got %d", len(raw.Mailboxes))
	}
	for _, key := range []string{"lane", "lane_reason"} {
		if _, present := raw.Mailboxes[0][key]; !present {
			t.Fatalf("%q is absent from the response; the schema requires it and the SPA falls back to probation without it", key)
		}
	}
	if got := raw.Mailboxes[0]["lane"]; got != "quarantine" {
		t.Fatalf("lane = %v, want quarantine — the participant's real lane, not a default", got)
	}
	if got, _ := raw.Mailboxes[0]["lane_reason"].(string); got == "" {
		t.Fatal("lane_reason is empty; a withheld mailbox must say why")
	}
}

// The tabbed pair is asserted on the RAW JSON for the reason the lane axis is: the
// UI has to tell "no tab was ever detectable here" from "tabs were detectable and
// none were used", and BOTH of those arrive as a falsy value through a decoded
// struct. tabbed_rate_7d must be present and NULL with an empty denominator — never
// 0.0, which reads as a confident clean rate for a mailbox whose tabs are merely
// invisible.
//
// The keys are asserted present even though the schema does not list them as
// required, because an absent key is the same `undefined` the lane bug produced: a
// client's safe fallback then hides the difference the field exists to express.
func TestOverviewCarriesTheTabbedPairAndNullsAnUnmeasurableRate(t *testing.T) {
	ws := uuid.New()
	store := newFakeStore()
	store.enabledCount = 2
	store.overviewRows = []OverviewRow{
		{
			MailboxID: uuid.New(), Enabled: true, StartVolume: 4, MaxVolume: 40, RampIncrement: 2,
			StartedAt:   pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
			HealthState: "healthy", Lane: "healthy", Email: "gmail@example.com",
			Inbox7d: 10, Spam7d: 0, Tabbed7d: 4, TabCapable7d: 10, TodaySent: 2,
		},
		{
			MailboxID: uuid.New(), Enabled: true, StartVolume: 4, MaxVolume: 40, RampIncrement: 2,
			StartedAt:   pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
			HealthState: "healthy", Lane: "healthy", Email: "imap@example.com",
			Inbox7d: 10, Spam7d: 0, Tabbed7d: 0, TabCapable7d: 0, TodaySent: 2,
		},
	}
	h := NewHandler(NewService(store))

	w := do(t, authedRouter(h), http.MethodGet, "/warmup/overview", bearer(t, ws), "")
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	var raw struct {
		Mailboxes []map[string]any `json:"mailboxes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(raw.Mailboxes) != 2 {
		t.Fatalf("want two mailboxes, got %d", len(raw.Mailboxes))
	}
	for i, mailbox := range raw.Mailboxes {
		for _, key := range []string{"tabbed_rate_7d", "tab_capable_sample_7d"} {
			if _, present := mailbox[key]; !present {
				t.Fatalf("mailbox %d: %q is absent from the response; an absent key reads as undefined, "+
					"which the client cannot tell from a measured value", i, key)
			}
		}
	}

	gmail, imap := raw.Mailboxes[0], raw.Mailboxes[1]
	if got := gmail["tabbed_rate_7d"]; got != 0.4 {
		t.Errorf("gmail tabbed_rate_7d = %v, want 0.4 (4 of 10 tab-capable)", got)
	}
	if got := gmail["tab_capable_sample_7d"]; got != float64(10) {
		t.Errorf("gmail tab_capable_sample_7d = %v, want 10", got)
	}
	if got := imap["tabbed_rate_7d"]; got != nil {
		t.Errorf("imap tabbed_rate_7d = %v, want null: nothing observing this mailbox could report a category, "+
			"and a zero would read as a confident clean rate", got)
	}
	if got := imap["tab_capable_sample_7d"]; got != float64(0) {
		t.Errorf("imap tab_capable_sample_7d = %v, want 0", got)
	}
}

// TestTransitionsRouteReturnsThePage proves the contract path, the JSON envelope
// and the field names the SPA generates against: an object with a `transitions`
// array whose rows carry snake_case keys and null (not "") lane fields on a
// pre-lane row.
func TestTransitionsRouteReturnsThePage(t *testing.T) {
	ws, mb := uuid.New(), uuid.New()
	store := newFakeStore()
	store.ownedMailboxes[mb] = ws
	store.transitions[mb] = []Transition{{
		ID: uuid.New(), CreatedAt: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
		FromState: "unknown", ToState: "healthy",
		ReasonCode: "evidence_qualified", Reason: "qualified placement evidence establishes health",
		PolicyVersion: "warmup-phase1-v1",
	}}
	h := NewHandler(NewService(store))

	w := do(t, authedRouter(h), http.MethodGet, "/warmup/mailboxes/"+mb.String()+"/transitions", bearer(t, ws), "")
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	var raw struct {
		Transitions []map[string]any `json:"transitions"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(raw.Transitions) != 1 {
		t.Fatalf("want 1 transition, got %d: %s", len(raw.Transitions), w.Body.String())
	}
	row := raw.Transitions[0]
	for _, key := range []string{
		"id", "created_at", "from_state", "to_state", "reason_code", "reason",
		"placement_samples", "spam_rate", "bounce_population", "bounce_samples", "bounce_rate",
		"complaint_samples", "complaint_rate", "invalid_tokens", "policy_version",
	} {
		if _, ok := row[key]; !ok {
			t.Fatalf("required field %q missing from the payload: %s", key, w.Body.String())
		}
	}
	if row["from_lane"] != nil || row["to_lane"] != nil {
		t.Fatalf("pre-lane row must send null lanes, got %v/%v", row["from_lane"], row["to_lane"])
	}
	// Same reasoning on the bounce axis: a row from before the split does not know
	// which population its samples counted, so it says null rather than guessing.
	if row["bounce_population"] != nil {
		t.Fatalf("pre-split row must send a null bounce_population, got %v", row["bounce_population"])
	}
}

// TestTransitionsForeignMailboxIs404 proves the endpoint is workspace-pinned at
// the HTTP seam: the workspace comes from the JWT, and a mailbox belonging to
// another tenant is simply not there.
func TestTransitionsForeignMailboxIs404(t *testing.T) {
	ws, other, mb := uuid.New(), uuid.New(), uuid.New()
	store := newFakeStore()
	store.ownedMailboxes[mb] = other
	store.transitions[mb] = []Transition{{ID: uuid.New(), FromState: "healthy", ToState: "watch"}}
	h := NewHandler(NewService(store))

	w := do(t, authedRouter(h), http.MethodGet, "/warmup/mailboxes/"+mb.String()+"/transitions", bearer(t, ws), "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "watch") {
		t.Fatalf("404 body leaked another tenant's transition: %s", w.Body.String())
	}
}

// TestTransitionsRejectsANonNumericLimit proves a malformed page size is a
// caller error rather than something silently reinterpreted as the default.
func TestTransitionsRejectsANonNumericLimit(t *testing.T) {
	ws, mb := uuid.New(), uuid.New()
	store := newFakeStore()
	store.ownedMailboxes[mb] = ws
	h := NewHandler(NewService(store))

	w := do(t, authedRouter(h), http.MethodGet,
		"/warmup/mailboxes/"+mb.String()+"/transitions?limit=all", bearer(t, ws), "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", w.Code, w.Body.String())
	}
}

// TestTransitionsRequiresAuth proves the endpoint is inside the authenticated
// group: no bearer, no history.
func TestTransitionsRequiresAuth(t *testing.T) {
	mb := uuid.New()
	h := NewHandler(NewService(newFakeStore()))
	w := do(t, authedRouter(h), http.MethodGet, "/warmup/mailboxes/"+mb.String()+"/transitions", "", "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
}
