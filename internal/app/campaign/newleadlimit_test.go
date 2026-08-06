package campaign

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

func TestGetScheduleReportsTheStoredMaxNewLeadsPerDay(t *testing.T) {
	ws, id := uuid.New(), uuid.New()
	stored := int32(40)
	store := &fakeStore{
		campaigns: map[[2]uuid.UUID]gen.Campaign{{ws, id}: {ID: id, Timezone: "UTC", MaxNewLeadsPerDay: &stored}},
		windows:   []SendWindow{{Weekday: 1, StartMinute: 540, EndMinute: 1020}},
	}
	svc := NewService(store, okChecker{active: true})

	got, err := svc.GetSchedule(context.Background(), ws, id)
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if got.MaxNewLeadsPerDay == nil || *got.MaxNewLeadsPerDay != 40 {
		t.Errorf("max new leads per day = %v, want 40", got.MaxNewLeadsPerDay)
	}
}

// No limit is nil, never zero: a campaign that has never set one sends whatever
// its mailboxes and any daily_limit allow, unrestricted on new-contact volume.
func TestGetScheduleReportsNoMaxNewLeadsPerDayAsNil(t *testing.T) {
	ws, id := uuid.New(), uuid.New()
	store := &fakeStore{
		campaigns: map[[2]uuid.UUID]gen.Campaign{{ws, id}: {ID: id, Timezone: "UTC"}},
		windows:   []SendWindow{{Weekday: 1, StartMinute: 540, EndMinute: 1020}},
	}
	svc := NewService(store, okChecker{active: true})

	got, err := svc.GetSchedule(context.Background(), ws, id)
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if got.MaxNewLeadsPerDay != nil {
		t.Errorf("max new leads per day = %v, want nil", *got.MaxNewLeadsPerDay)
	}
}

func TestSetScheduleRejectsAnUnusableMaxNewLeadsPerDayWithoutWriting(t *testing.T) {
	for _, limit := range []int{0, -1, maxNewLeadsPerDay + 1, 99999999999} {
		ws, id := uuid.New(), uuid.New()
		store := &fakeStore{campaigns: map[[2]uuid.UUID]gen.Campaign{{ws, id}: {ID: id}}}
		svc := NewService(store, okChecker{active: true})

		in := Plan{
			Schedule:          Schedule{Timezone: "UTC", Windows: []SendWindow{{Weekday: 1, StartMinute: 540, EndMinute: 1020}}},
			MaxNewLeadsPerDay: &limit,
		}
		if _, err := svc.SetSchedule(context.Background(), ws, id, in); !errors.Is(err, ErrMaxNewLeadsPerDay) {
			t.Errorf("limit %d: err = %v, want ErrMaxNewLeadsPerDay", limit, err)
		}
		// Validation precedes persistence: a rejected plan leaves the previous one.
		if store.replacedSchedule != nil {
			t.Errorf("limit %d was persisted: %+v", limit, *store.replacedSchedule)
		}
	}
}

// The bounds are inclusive: both ends of the contract's range must save.
func TestSetScheduleAcceptsTheMaxNewLeadsPerDayBoundaries(t *testing.T) {
	for _, limit := range []int{minNewLeadsPerDay, maxNewLeadsPerDay} {
		ws, id := uuid.New(), uuid.New()
		store := &fakeStore{campaigns: map[[2]uuid.UUID]gen.Campaign{{ws, id}: {ID: id}}}
		svc := NewService(store, okChecker{active: true})

		in := Plan{
			Schedule:          Schedule{Timezone: "UTC", Windows: []SendWindow{{Weekday: 1, StartMinute: 540, EndMinute: 1020}}},
			MaxNewLeadsPerDay: &limit,
		}
		if _, err := svc.SetSchedule(context.Background(), ws, id, in); err != nil {
			t.Errorf("limit %d rejected: %v", limit, err)
		}
	}
}

func TestSetSchedulePersistsAndClearsTheMaxNewLeadsPerDay(t *testing.T) {
	ws, id := uuid.New(), uuid.New()
	store := &fakeStore{campaigns: map[[2]uuid.UUID]gen.Campaign{{ws, id}: {ID: id}}}
	svc := NewService(store, okChecker{active: true})
	sched := Schedule{Timezone: "UTC", Windows: []SendWindow{{Weekday: 1, StartMinute: 540, EndMinute: 1020}}}

	limit := 30
	got, err := svc.SetSchedule(context.Background(), ws, id, Plan{Schedule: sched, MaxNewLeadsPerDay: &limit})
	if err != nil {
		t.Fatalf("SetSchedule: %v", err)
	}
	if got.MaxNewLeadsPerDay == nil || *got.MaxNewLeadsPerDay != 30 {
		t.Errorf("returned limit = %v, want 30", got.MaxNewLeadsPerDay)
	}
	if store.replacedSchedule == nil || store.replacedSchedule.MaxNewLeadsPerDay == nil ||
		*store.replacedSchedule.MaxNewLeadsPerDay != 30 {
		t.Fatalf("persisted plan = %+v, want a limit of 30", store.replacedSchedule)
	}

	// A nil limit on the next save clears it — that is how the panel's empty field
	// means "no new-lead limit".
	if _, err := svc.SetSchedule(context.Background(), ws, id, Plan{Schedule: sched}); err != nil {
		t.Fatalf("SetSchedule: %v", err)
	}
	if store.replacedSchedule.MaxNewLeadsPerDay != nil {
		t.Errorf("persisted limit = %v, want nil after clearing", *store.replacedSchedule.MaxNewLeadsPerDay)
	}
}

// The wire contract: max_new_leads_per_day round-trips through PUT and is present
// (as null when unset) on the response, since the client reads it back into the
// field. daily_limit is set alongside it to prove the two ceilings save
// independently in the same full-replace PUT.
func TestPutScheduleRoundTripsMaxNewLeadsPerDay(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	ws, id := uuid.New(), uuid.New()
	store := &fakeStore{campaigns: map[[2]uuid.UUID]gen.Campaign{{ws, id}: {ID: id, WorkspaceID: ws, Timezone: "UTC"}}}
	h := NewHandler(NewService(store, okChecker{active: true}), &fakeEnqueuer{})

	body := `{"timezone":"UTC","daily_limit":250,"max_new_leads_per_day":25,"days":[{"weekday":1,"intervals":[{"start_minute":540,"end_minute":1020}]}]}`
	w := httptest.NewRecorder()
	req := newScheduleRequest(t, secret, ws, id, http.MethodPut, body)
	auth.RequireAuth(auth.NewJWTVerifier(secret))(http.HandlerFunc(h.putSchedule)).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	var got struct {
		DailyLimit        *int `json:"daily_limit"`
		MaxNewLeadsPerDay *int `json:"max_new_leads_per_day"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.MaxNewLeadsPerDay == nil || *got.MaxNewLeadsPerDay != 25 {
		t.Errorf("max_new_leads_per_day = %v, want 25", got.MaxNewLeadsPerDay)
	}
	if got.DailyLimit == nil || *got.DailyLimit != 250 {
		t.Errorf("daily_limit = %v, want 250 (independent of the new-lead throttle)", got.DailyLimit)
	}
	if store.replacedSchedule == nil || store.replacedSchedule.MaxNewLeadsPerDay == nil {
		t.Fatal("the limit did not reach the store")
	}
}

// A limit outside [1, 1000000] is a 422: the field is well-formed, its value is
// unacceptable. Mirrors the daily_limit contract exactly.
func TestPutScheduleRejectsAMaxNewLeadsPerDayOutsideItsRange(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	for _, body := range []string{
		`{"timezone":"UTC","max_new_leads_per_day":0,"days":[{"weekday":1,"intervals":[{"start_minute":540,"end_minute":1020}]}]}`,
		`{"timezone":"UTC","max_new_leads_per_day":-5,"days":[{"weekday":1,"intervals":[{"start_minute":540,"end_minute":1020}]}]}`,
		`{"timezone":"UTC","max_new_leads_per_day":1000001,"days":[{"weekday":1,"intervals":[{"start_minute":540,"end_minute":1020}]}]}`,
		`{"timezone":"UTC","max_new_leads_per_day":99999999999,"days":[{"weekday":1,"intervals":[{"start_minute":540,"end_minute":1020}]}]}`,
	} {
		ws, id := uuid.New(), uuid.New()
		store := &fakeStore{campaigns: map[[2]uuid.UUID]gen.Campaign{{ws, id}: {ID: id, WorkspaceID: ws, Timezone: "UTC"}}}
		h := NewHandler(NewService(store, okChecker{active: true}), &fakeEnqueuer{})

		w := httptest.NewRecorder()
		req := newScheduleRequest(t, secret, ws, id, http.MethodPut, body)
		auth.RequireAuth(auth.NewJWTVerifier(secret))(http.HandlerFunc(h.putSchedule)).ServeHTTP(w, req)

		if w.Code != http.StatusUnprocessableEntity {
			t.Errorf("body %s: want 422, got %d: %s", body, w.Code, w.Body.String())
		}
		if store.replacedSchedule != nil {
			t.Errorf("body %s: a rejected limit was persisted", body)
		}
	}
}

// An omitted max_new_leads_per_day clears the limit rather than failing: the
// generated client omits the field when the panel's input is empty, and this is
// a full-replace PUT.
func TestPutScheduleWithoutMaxNewLeadsPerDayClearsIt(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	ws, id := uuid.New(), uuid.New()
	store := &fakeStore{campaigns: map[[2]uuid.UUID]gen.Campaign{{ws, id}: {ID: id, WorkspaceID: ws, Timezone: "UTC"}}}
	h := NewHandler(NewService(store, okChecker{active: true}), &fakeEnqueuer{})

	body := `{"timezone":"UTC","days":[{"weekday":1,"intervals":[{"start_minute":540,"end_minute":1020}]}]}`
	w := httptest.NewRecorder()
	req := newScheduleRequest(t, secret, ws, id, http.MethodPut, body)
	auth.RequireAuth(auth.NewJWTVerifier(secret))(http.HandlerFunc(h.putSchedule)).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if store.replacedSchedule == nil {
		t.Fatal("the plan did not reach the store")
	}
	if store.replacedSchedule.MaxNewLeadsPerDay != nil {
		t.Errorf("persisted limit = %v, want nil", *store.replacedSchedule.MaxNewLeadsPerDay)
	}
}
