package campaign

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/sendcap"
)

func TestGetScheduleReportsTheStoredDailyLimit(t *testing.T) {
	ws, id := uuid.New(), uuid.New()
	stored := int32(250)
	store := &fakeStore{
		campaigns: map[[2]uuid.UUID]gen.Campaign{{ws, id}: {ID: id, Timezone: "UTC", DailyLimit: &stored}},
		windows:   []SendWindow{{Weekday: 1, StartMinute: 540, EndMinute: 1020}},
	}
	svc := NewService(store, okChecker{active: true})

	got, err := svc.GetSchedule(context.Background(), ws, id)
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if got.DailyLimit == nil || *got.DailyLimit != 250 {
		t.Errorf("daily limit = %v, want 250", got.DailyLimit)
	}
}

// No limit is nil, never zero: a campaign that has never set one behaves exactly as
// it did before the column existed.
func TestGetScheduleReportsNoLimitAsNil(t *testing.T) {
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
	if got.DailyLimit != nil {
		t.Errorf("daily limit = %v, want nil", *got.DailyLimit)
	}
}

func TestSetScheduleRejectsAnUnusableDailyLimitWithoutWriting(t *testing.T) {
	for _, limit := range []int{0, -1, maxDailyLimit + 1} {
		ws, id := uuid.New(), uuid.New()
		store := &fakeStore{campaigns: map[[2]uuid.UUID]gen.Campaign{{ws, id}: {ID: id}}}
		svc := NewService(store, okChecker{active: true})

		in := Plan{
			Schedule:   Schedule{Timezone: "UTC", Windows: []SendWindow{{Weekday: 1, StartMinute: 540, EndMinute: 1020}}},
			DailyLimit: &limit,
		}
		if _, err := svc.SetSchedule(context.Background(), ws, id, in); !errors.Is(err, ErrDailyLimit) {
			t.Errorf("limit %d: err = %v, want ErrDailyLimit", limit, err)
		}
		// Validation precedes persistence: a rejected plan leaves the previous one.
		if store.replacedSchedule != nil {
			t.Errorf("limit %d was persisted: %+v", limit, *store.replacedSchedule)
		}
	}
}

func TestSetSchedulePersistsAndClearsTheDailyLimit(t *testing.T) {
	ws, id := uuid.New(), uuid.New()
	store := &fakeStore{campaigns: map[[2]uuid.UUID]gen.Campaign{{ws, id}: {ID: id}}}
	svc := NewService(store, okChecker{active: true})
	sched := Schedule{Timezone: "UTC", Windows: []SendWindow{{Weekday: 1, StartMinute: 540, EndMinute: 1020}}}

	limit := 100
	got, err := svc.SetSchedule(context.Background(), ws, id, Plan{Schedule: sched, DailyLimit: &limit})
	if err != nil {
		t.Fatalf("SetSchedule: %v", err)
	}
	if got.DailyLimit == nil || *got.DailyLimit != 100 {
		t.Errorf("returned limit = %v, want 100", got.DailyLimit)
	}
	if store.replacedSchedule == nil || store.replacedSchedule.DailyLimit == nil ||
		*store.replacedSchedule.DailyLimit != 100 {
		t.Fatalf("persisted plan = %+v, want a limit of 100", store.replacedSchedule)
	}

	// A nil limit on the next save clears it — that is how the panel's empty field
	// means "no campaign limit".
	if _, err := svc.SetSchedule(context.Background(), ws, id, Plan{Schedule: sched}); err != nil {
		t.Fatalf("SetSchedule: %v", err)
	}
	if store.replacedSchedule.DailyLimit != nil {
		t.Errorf("persisted limit = %v, want nil after clearing", *store.replacedSchedule.DailyLimit)
	}
}

// The wire contract: daily_limit round-trips through PUT and is present (as null
// when unset) on the response, since the client reads it back into the field.
func TestPutScheduleRoundTripsDailyLimit(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	ws, id := uuid.New(), uuid.New()
	store := &fakeStore{campaigns: map[[2]uuid.UUID]gen.Campaign{{ws, id}: {ID: id, WorkspaceID: ws, Timezone: "UTC"}}}
	h := NewHandler(NewService(store, okChecker{active: true}), &fakeEnqueuer{})

	body := `{"timezone":"UTC","daily_limit":40,"days":[{"weekday":1,"intervals":[{"start_minute":540,"end_minute":1020}]}]}`
	w := httptest.NewRecorder()
	req := newScheduleRequest(t, secret, ws, id, http.MethodPut, body)
	auth.RequireAuth(auth.NewJWTVerifier(secret))(http.HandlerFunc(h.putSchedule)).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	var got struct {
		DailyLimit *int `json:"daily_limit"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.DailyLimit == nil || *got.DailyLimit != 40 {
		t.Errorf("daily_limit = %v, want 40", got.DailyLimit)
	}
	if store.replacedSchedule == nil || store.replacedSchedule.DailyLimit == nil {
		t.Fatal("the limit did not reach the store")
	}
}

// A limit below 1 is a 422, not a 400 and not a silently-clamped save: the field is
// well-formed, its value is unacceptable.
func TestPutScheduleRejectsALimitBelowOne(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	for _, body := range []string{
		`{"timezone":"UTC","daily_limit":0,"days":[{"weekday":1,"intervals":[{"start_minute":540,"end_minute":1020}]}]}`,
		`{"timezone":"UTC","daily_limit":-5,"days":[{"weekday":1,"intervals":[{"start_minute":540,"end_minute":1020}]}]}`,
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

// An omitted daily_limit clears the limit rather than failing: the generated client
// omits the field when the panel's input is empty.
func TestPutScheduleWithoutDailyLimitClearsIt(t *testing.T) {
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
	if store.replacedSchedule.DailyLimit != nil {
		t.Errorf("persisted limit = %v, want nil", *store.replacedSchedule.DailyLimit)
	}
}

// The senders panel exists so a gated campaign explains itself. cap_today must be
// the cap the SEND path will enforce, which means the same ramp-then-health
// arithmetic — a panel promising 40 while the sender allows 20 is worse than no
// panel at all.
func TestSenderCapacityReportsTheEnforcedCapAndReason(t *testing.T) {
	connected := pgtype.Timestamptz{Time: time.Now().Add(-90 * 24 * time.Hour), Valid: true}
	base := senderCapacity{
		dailyCap: 40, rampStartCap: 5, rampDays: 30, rampEnabled: true,
		mailboxCreatedAt: connected, mailboxStatus: "active", poolEnabled: true, sentToday: 12,
	}
	for _, tc := range []struct {
		name        string
		mutate      func(*senderCapacity)
		wantCap     int
		wantSending bool
		wantHealth  *string
	}{
		{name: "not warming up sends at its full ramped cap", mutate: func(*senderCapacity) {}, wantCap: 40, wantSending: true},
		{
			name:   "watch keeps 70 percent and still sends",
			mutate: func(c *senderCapacity) { c.healthState = sendcap.HealthWatch },
			// The panel must show the reduced cap, or the operator sees a campaign
			// slowing down with nothing on screen explaining it.
			wantCap: 28, wantSending: true, wantHealth: ptr(sendcap.HealthWatch),
		},
		{
			name:   "throttled keeps half",
			mutate: func(c *senderCapacity) { c.healthState = sendcap.HealthThrottled },
			// The panel must show the reduced cap, or the operator sees a campaign
			// slowing down with nothing on screen explaining it.
			wantCap: 20, wantSending: true, wantHealth: ptr(sendcap.HealthThrottled),
		},
		{
			name:   "paused is not sending at all",
			mutate: func(c *senderCapacity) { c.healthState = sendcap.HealthPaused },
			// cap 0 + sending false is the row the UI turns into "paused by warmup".
			wantCap: 0, wantSending: false, wantHealth: ptr(sendcap.HealthPaused),
		},
		{
			name:   "an excluded pool member is not sending",
			mutate: func(c *senderCapacity) { c.poolEnabled = false },
			// Its cap is untouched — it is held out of rotation, not degraded.
			wantCap: 40, wantSending: false,
		},
		{
			name:    "an inactive mailbox is not sending",
			mutate:  func(c *senderCapacity) { c.mailboxStatus = "disconnected" },
			wantCap: 40, wantSending: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := base
			tc.mutate(&in)
			var got Sender
			in.fill(&got)

			if got.CapToday != tc.wantCap {
				t.Errorf("cap_today = %d, want %d", got.CapToday, tc.wantCap)
			}
			if got.Sending != tc.wantSending {
				t.Errorf("sending = %v, want %v", got.Sending, tc.wantSending)
			}
			if got.SentToday != 12 {
				t.Errorf("sent_today = %d, want 12", got.SentToday)
			}
			switch {
			case tc.wantHealth == nil && got.HealthState != nil:
				t.Errorf("health_state = %q, want null (not warming up)", *got.HealthState)
			case tc.wantHealth != nil && (got.HealthState == nil || *got.HealthState != *tc.wantHealth):
				t.Errorf("health_state = %v, want %q", got.HealthState, *tc.wantHealth)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }
