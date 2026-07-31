package campaign

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// newScheduleRequest builds an authed request against /campaigns/{id}/schedule,
// routed through RequireAuth exactly as the protected group does.
func newScheduleRequest(t *testing.T, secret []byte, ws, campaignID uuid.UUID, method, body string) *http.Request {
	t.Helper()
	tok, err := auth.IssueToken(secret, auth.Claims{
		UserID: uuid.New().String(), WorkspaceID: ws.String(), Role: "owner", SessionID: uuid.New().String(),
	}, time.Hour)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	var reader io.Reader = http.NoBody
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, "/campaigns/"+campaignID.String()+"/schedule", reader)
	req.Header.Set("Authorization", "Bearer "+tok)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", campaignID.String())
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func TestGetScheduleGroupsWindowsByWeekdayWithPreview(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	ws, id := uuid.New(), uuid.New()
	store := &fakeStore{
		campaigns: map[[2]uuid.UUID]gen.Campaign{{ws, id}: {ID: id, WorkspaceID: ws, Timezone: "UTC"}},
		windows: []SendWindow{
			{Weekday: 1, StartMinute: 540, EndMinute: 720},
			{Weekday: 1, StartMinute: 780, EndMinute: 1020},
			{Weekday: 3, StartMinute: 540, EndMinute: 1020},
		},
	}
	h := NewHandler(NewService(store, okChecker{active: true}), &fakeEnqueuer{})

	w := httptest.NewRecorder()
	req := newScheduleRequest(t, secret, ws, id, http.MethodGet, "")
	auth.RequireAuth(auth.NewJWTVerifier(secret))(http.HandlerFunc(h.getSchedule)).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp scheduleResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Timezone != "UTC" {
		t.Errorf("timezone = %q, want UTC", resp.Timezone)
	}
	if len(resp.Days) != 2 {
		t.Fatalf("days = %d, want 2 (Monday and Wednesday)", len(resp.Days))
	}
	if resp.Days[0].Weekday != 1 || len(resp.Days[0].Intervals) != 2 {
		t.Errorf("Monday = %+v, want weekday 1 with two intervals", resp.Days[0])
	}
	if resp.Days[1].Weekday != 3 || len(resp.Days[1].Intervals) != 1 {
		t.Errorf("Wednesday = %+v, want weekday 3 with one interval", resp.Days[1])
	}
	if len(resp.Preview) != previewCount {
		t.Errorf("preview = %d entries, want %d", len(resp.Preview), previewCount)
	}
	// The preview exists to show the cadence is off the grid; ":00" seconds in
	// every entry would mean the humanization never reached the response.
	for _, p := range resp.Preview {
		if strings.HasSuffix(p, ":00") {
			t.Errorf("preview entry %q lands on a clock boundary", p)
		}
	}
}

func TestPutScheduleReplacesAndEchoesTheSchedule(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	ws, id := uuid.New(), uuid.New()
	store := &fakeStore{campaigns: map[[2]uuid.UUID]gen.Campaign{{ws, id}: {ID: id, WorkspaceID: ws}}}
	h := NewHandler(NewService(store, okChecker{active: true}), &fakeEnqueuer{})

	body := `{"timezone":"America/New_York","days":[
	  {"weekday":2,"intervals":[{"start_minute":600,"end_minute":900}]},
	  {"weekday":4,"intervals":[{"start_minute":540,"end_minute":720}]}
	]}`
	w := httptest.NewRecorder()
	req := newScheduleRequest(t, secret, ws, id, http.MethodPut, body)
	auth.RequireAuth(auth.NewJWTVerifier(secret))(http.HandlerFunc(h.putSchedule)).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if store.replacedSchedule == nil {
		t.Fatal("schedule was not persisted")
	}
	if store.replacedSchedule.Timezone != "America/New_York" {
		t.Errorf("persisted timezone = %q", store.replacedSchedule.Timezone)
	}
	if len(store.replacedSchedule.Windows) != 2 {
		t.Errorf("persisted windows = %+v, want 2", store.replacedSchedule.Windows)
	}
	var resp scheduleResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Days) != 2 {
		t.Errorf("echoed days = %d, want 2", len(resp.Days))
	}
}

func TestPutScheduleRejectsBadInput(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		wantCode int
	}{
		{"malformed json", `{"timezone":`, http.StatusBadRequest},
		{
			name:     "unknown timezone",
			body:     `{"timezone":"Mars/Olympus_Mons","days":[{"weekday":1,"intervals":[{"start_minute":540,"end_minute":1020}]}]}`,
			wantCode: http.StatusUnprocessableEntity,
		},
		{
			name:     "nothing open all week",
			body:     `{"timezone":"UTC","days":[]}`,
			wantCode: http.StatusUnprocessableEntity,
		},
		{
			name: "overlapping intervals",
			body: `{"timezone":"UTC","days":[{"weekday":1,"intervals":[
			  {"start_minute":540,"end_minute":720},{"start_minute":700,"end_minute":900}]}]}`,
			wantCode: http.StatusUnprocessableEntity,
		},
		{
			name:     "inverted interval",
			body:     `{"timezone":"UTC","days":[{"weekday":1,"intervals":[{"start_minute":1020,"end_minute":540}]}]}`,
			wantCode: http.StatusUnprocessableEntity,
		},
	}

	secret := []byte("0123456789abcdef0123456789abcdef")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ws, id := uuid.New(), uuid.New()
			store := &fakeStore{campaigns: map[[2]uuid.UUID]gen.Campaign{{ws, id}: {ID: id, WorkspaceID: ws}}}
			h := NewHandler(NewService(store, okChecker{active: true}), &fakeEnqueuer{})

			w := httptest.NewRecorder()
			req := newScheduleRequest(t, secret, ws, id, http.MethodPut, tc.body)
			auth.RequireAuth(auth.NewJWTVerifier(secret))(http.HandlerFunc(h.putSchedule)).ServeHTTP(w, req)

			if w.Code != tc.wantCode {
				t.Fatalf("code = %d, want %d: %s", w.Code, tc.wantCode, w.Body.String())
			}
			if store.replacedSchedule != nil {
				t.Errorf("rejected schedule was persisted: %+v", *store.replacedSchedule)
			}
		})
	}
}

// A campaign in another workspace must 404, not leak its schedule.
func TestScheduleEndpointsAreWorkspaceScoped(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	ownerWS, id := uuid.New(), uuid.New()
	store := &fakeStore{campaigns: map[[2]uuid.UUID]gen.Campaign{{ownerWS, id}: {ID: id, WorkspaceID: ownerWS}}}
	h := NewHandler(NewService(store, okChecker{active: true}), &fakeEnqueuer{})
	intruder := uuid.New()

	for _, tc := range []struct {
		name    string
		method  string
		body    string
		handler http.HandlerFunc
	}{
		{"get", http.MethodGet, "", h.getSchedule},
		{"put", http.MethodPut, `{"timezone":"UTC","days":[{"weekday":1,"intervals":[{"start_minute":540,"end_minute":1020}]}]}`, h.putSchedule},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := newScheduleRequest(t, secret, intruder, id, tc.method, tc.body)
			auth.RequireAuth(auth.NewJWTVerifier(secret))(tc.handler).ServeHTTP(w, req)

			if w.Code != http.StatusNotFound {
				t.Fatalf("code = %d, want 404", w.Code)
			}
		})
	}
}
