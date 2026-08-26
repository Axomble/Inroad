package inbox_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/inbox"
)

// fakeSnoozeStore is an in-memory SnoozeStore. It enforces the same two rules
// the schema does — one snooze per thread (a map keyed by thread id, so a
// re-snooze replaces) and workspace isolation on every read — so a test
// asserting either is asserting real behavior rather than a stub's convenience.
type fakeSnoozeStore struct {
	snoozes map[uuid.UUID]inbox.Snooze
	// upsertErr, when non-nil, fails every write — for the "the store is
	// broken" branch, which no amount of valid input can otherwise reach.
	upsertErr error
	getErr    error
}

func newFakeSnoozeStore() *fakeSnoozeStore {
	return &fakeSnoozeStore{snoozes: map[uuid.UUID]inbox.Snooze{}}
}

func (f *fakeSnoozeStore) UpsertSnooze(_ context.Context, in inbox.UpsertSnoozeInput) (inbox.Snooze, error) {
	if f.upsertErr != nil {
		return inbox.Snooze{}, f.upsertErr
	}
	s := inbox.Snooze{
		ThreadID:    in.ThreadID,
		WorkspaceID: in.WorkspaceID,
		SnoozeUntil: in.SnoozeUntil,
		SnoozedBy:   in.SnoozedBy,
		CreatedAt:   time.Now().UTC(),
	}
	f.snoozes[in.ThreadID] = s
	return s, nil
}

func (f *fakeSnoozeStore) DeleteSnooze(_ context.Context, ws, threadID uuid.UUID) error {
	s, ok := f.snoozes[threadID]
	if !ok || s.WorkspaceID != ws {
		return inbox.ErrNotFound
	}
	delete(f.snoozes, threadID)
	return nil
}

func (f *fakeSnoozeStore) GetSnooze(_ context.Context, ws, threadID uuid.UUID) (inbox.Snooze, error) {
	if f.getErr != nil {
		return inbox.Snooze{}, f.getErr
	}
	s, ok := f.snoozes[threadID]
	if !ok || s.WorkspaceID != ws {
		return inbox.Snooze{}, inbox.ErrNotFound
	}
	return s, nil
}

func (f *fakeSnoozeStore) CountSnoozed(_ context.Context, ws uuid.UUID) (int64, error) {
	var n int64
	for _, s := range f.snoozes {
		if s.WorkspaceID == ws && s.SnoozeUntil.After(time.Now()) {
			n++
		}
	}
	return n, nil
}

// snoozeFixture wires a Service over both fakes with a FIXED clock, so every
// horizon assertion below is exact rather than racing the real one.
type snoozeFixture struct {
	svc     *inbox.Service
	handler *inbox.Handler
	threads *fakeStore
	snoozes *fakeSnoozeStore
	now     time.Time
	thread  inbox.Thread
}

func newSnoozeFixture(t *testing.T) *snoozeFixture {
	t.Helper()
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	threads, snoozes := newFakeStore(), newFakeSnoozeStore()
	svc := inbox.NewService(threads,
		inbox.WithSnoozeStore(snoozes),
		inbox.WithClock(func() time.Time { return now }),
	)
	th, err := threads.UpsertThread(context.Background(), inbox.UpsertThreadInput{
		WorkspaceID: testWS, MailboxID: uuid.New(), RootMessageID: "<snooze@s.test>", Subject: "S",
	})
	if err != nil {
		t.Fatalf("seed thread: %v", err)
	}
	return &snoozeFixture{
		svc: svc, handler: inbox.NewHandler(svc),
		threads: threads, snoozes: snoozes, now: now, thread: th,
	}
}

func TestSnoozeStoresTheChosenMoment(t *testing.T) {
	f := newSnoozeFixture(t)
	until := f.now.Add(48 * time.Hour)

	got, err := f.svc.Snooze(context.Background(), inbox.UpsertSnoozeInput{
		WorkspaceID: testWS, ThreadID: f.thread.ID, SnoozeUntil: until,
	})
	if err != nil {
		t.Fatalf("Snooze: %v", err)
	}
	if !got.SnoozeUntil.Equal(until) {
		t.Errorf("SnoozeUntil = %v, want %v", got.SnoozeUntil, until)
	}
	if !got.Active(f.now) {
		t.Error("a snooze 48h out is not reported active")
	}
}

func TestSnoozeRejectsAMomentInThePast(t *testing.T) {
	f := newSnoozeFixture(t)
	for _, until := range []time.Time{f.now.Add(-time.Hour), f.now} {
		_, err := f.svc.Snooze(context.Background(), inbox.UpsertSnoozeInput{
			WorkspaceID: testWS, ThreadID: f.thread.ID, SnoozeUntil: until,
		})
		if !errors.Is(err, inbox.ErrSnoozeInPast) {
			t.Errorf("Snooze(%v) error = %v, want ErrSnoozeInPast", until, err)
		}
	}
}

func TestSnoozeRejectsAMomentBeyondTheHorizon(t *testing.T) {
	f := newSnoozeFixture(t)
	_, err := f.svc.Snooze(context.Background(), inbox.UpsertSnoozeInput{
		WorkspaceID: testWS, ThreadID: f.thread.ID,
		SnoozeUntil: f.now.Add(inbox.SnoozeMaxHorizon + time.Minute),
	})
	if !errors.Is(err, inbox.ErrSnoozeTooFar) {
		t.Errorf("error = %v, want ErrSnoozeTooFar", err)
	}

	// Exactly at the horizon is allowed — the bound is inclusive.
	if _, err := f.svc.Snooze(context.Background(), inbox.UpsertSnoozeInput{
		WorkspaceID: testWS, ThreadID: f.thread.ID,
		SnoozeUntil: f.now.Add(inbox.SnoozeMaxHorizon),
	}); err != nil {
		t.Errorf("a snooze exactly at the horizon was rejected: %v", err)
	}
}

// Re-snoozing is an ordinary action (pushing something further out), not a
// conflict — and it must leave ONE snooze, not two.
func TestReSnoozeReplacesTheMoment(t *testing.T) {
	f := newSnoozeFixture(t)
	ctx := context.Background()
	first := f.now.Add(24 * time.Hour)
	second := f.now.Add(72 * time.Hour)

	if _, err := f.svc.Snooze(ctx, inbox.UpsertSnoozeInput{
		WorkspaceID: testWS, ThreadID: f.thread.ID, SnoozeUntil: first,
	}); err != nil {
		t.Fatalf("first Snooze: %v", err)
	}
	if _, err := f.svc.Snooze(ctx, inbox.UpsertSnoozeInput{
		WorkspaceID: testWS, ThreadID: f.thread.ID, SnoozeUntil: second,
	}); err != nil {
		t.Fatalf("second Snooze: %v", err)
	}

	got, err := f.svc.GetSnooze(ctx, testWS, f.thread.ID)
	if err != nil {
		t.Fatalf("GetSnooze: %v", err)
	}
	if !got.SnoozeUntil.Equal(second) {
		t.Errorf("SnoozeUntil = %v, want the re-snoozed %v", got.SnoozeUntil, second)
	}
	if len(f.snoozes.snoozes) != 1 {
		t.Errorf("%d snooze rows after re-snoozing, want 1", len(f.snoozes.snoozes))
	}
}

// Invariant: a foreign thread id must 404 from the thread lookup, never reach
// the insert (which would either leak the row's existence via a FK error or,
// worse, snooze another workspace's thread).
func TestSnoozeOnAForeignThreadIsNotFound(t *testing.T) {
	f := newSnoozeFixture(t)
	foreign, err := f.threads.UpsertThread(context.Background(), inbox.UpsertThreadInput{
		WorkspaceID: uuid.New(), MailboxID: uuid.New(), RootMessageID: "<foreign@s.test>",
	})
	if err != nil {
		t.Fatalf("seed foreign thread: %v", err)
	}

	_, err = f.svc.Snooze(context.Background(), inbox.UpsertSnoozeInput{
		WorkspaceID: testWS, ThreadID: foreign.ID, SnoozeUntil: f.now.Add(time.Hour),
	})
	if !errors.Is(err, inbox.ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
	if len(f.snoozes.snoozes) != 0 {
		t.Error("a foreign thread was snoozed anyway")
	}
}

func TestUnsnoozeRemovesIt(t *testing.T) {
	f := newSnoozeFixture(t)
	ctx := context.Background()
	if _, err := f.svc.Snooze(ctx, inbox.UpsertSnoozeInput{
		WorkspaceID: testWS, ThreadID: f.thread.ID, SnoozeUntil: f.now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("Snooze: %v", err)
	}
	if err := f.svc.Unsnooze(ctx, testWS, f.thread.ID); err != nil {
		t.Fatalf("Unsnooze: %v", err)
	}
	if _, err := f.svc.GetSnooze(ctx, testWS, f.thread.ID); !errors.Is(err, inbox.ErrNotFound) {
		t.Errorf("GetSnooze after Unsnooze = %v, want ErrNotFound", err)
	}
}

// A failed write must surface as a failure, not as a 200 with an unstored
// snooze — the operator would believe the thread was parked and lose it.
func TestSnoozeSurfacesAStoreWriteFailure(t *testing.T) {
	f := newSnoozeFixture(t)
	f.snoozes.upsertErr = errors.New("disk full")

	_, err := f.svc.Snooze(context.Background(), inbox.UpsertSnoozeInput{
		WorkspaceID: testWS, ThreadID: f.thread.ID, SnoozeUntil: f.now.Add(time.Hour),
	})
	if err == nil {
		t.Fatal("Snooze reported success despite a store failure")
	}

	res := serve(t, f.handler, http.MethodPut, "/inbox/threads/"+f.thread.ID.String()+"/snooze",
		`{"snooze_until":"`+f.now.Add(time.Hour).Format(time.RFC3339)+`"}`)
	if res.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 (%s)", res.Code, res.Body.String())
	}
}

func TestUnsnoozeAThreadThatIsNotSnoozedIsNotFound(t *testing.T) {
	f := newSnoozeFixture(t)
	err := f.svc.Unsnooze(context.Background(), testWS, f.thread.ID)
	if !errors.Is(err, inbox.ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}

// A lapsed snooze must read as "not snoozed" on the thread, without any
// sweeper having run — the whole reason the rule is evaluated at read time.
func TestGetThreadReportsALapsedSnoozeAsAbsent(t *testing.T) {
	f := newSnoozeFixture(t)
	ctx := context.Background()

	// Write a snooze directly, bypassing the Service's future-only validation,
	// to represent one that has since expired.
	f.snoozes.snoozes[f.thread.ID] = inbox.Snooze{
		ThreadID: f.thread.ID, WorkspaceID: testWS, SnoozeUntil: f.now.Add(-time.Hour),
	}

	detail, err := f.svc.GetThread(ctx, testWS, f.thread.ID)
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if detail.Snooze != nil {
		t.Errorf("Snooze = %+v, want nil for a lapsed snooze", detail.Snooze)
	}
}

func TestGetThreadCarriesAnActiveSnooze(t *testing.T) {
	f := newSnoozeFixture(t)
	ctx := context.Background()
	until := f.now.Add(24 * time.Hour)
	if _, err := f.svc.Snooze(ctx, inbox.UpsertSnoozeInput{
		WorkspaceID: testWS, ThreadID: f.thread.ID, SnoozeUntil: until,
	}); err != nil {
		t.Fatalf("Snooze: %v", err)
	}

	detail, err := f.svc.GetThread(ctx, testWS, f.thread.ID)
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if detail.Snooze == nil || !detail.Snooze.SnoozeUntil.Equal(until) {
		t.Errorf("Snooze = %+v, want one until %v", detail.Snooze, until)
	}
}

// A snooze-lookup failure must NOT degrade to "not snoozed": that would show an
// operator a Snooze button for an already-snoozed thread, and the re-snooze
// would overwrite a colleague's chosen moment.
func TestGetThreadPropagatesASnoozeLookupFailure(t *testing.T) {
	f := newSnoozeFixture(t)
	f.snoozes.getErr = errors.New("connection reset")

	if _, err := f.svc.GetThread(context.Background(), testWS, f.thread.ID); err == nil {
		t.Error("GetThread succeeded despite a snooze-store failure")
	}
}

// A Service with no snooze store configured must still serve threads — the
// dependency is optional, and every pre-snooze call site passes none.
func TestGetThreadWorksWithoutASnoozeStore(t *testing.T) {
	threads := newFakeStore()
	svc := inbox.NewService(threads)
	th, err := threads.UpsertThread(context.Background(), inbox.UpsertThreadInput{
		WorkspaceID: testWS, MailboxID: uuid.New(), RootMessageID: "<no-store@s.test>",
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	detail, err := svc.GetThread(context.Background(), testWS, th.ID)
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if detail.Snooze != nil {
		t.Errorf("Snooze = %+v, want nil", detail.Snooze)
	}
}

func TestListThreadsRejectsContradictorySnoozeFilters(t *testing.T) {
	f := newSnoozeFixture(t)
	_, err := f.svc.ListThreads(context.Background(), testWS, inbox.ListFilter{
		SnoozeHidden: true, SnoozedOnly: true,
	})
	if !errors.Is(err, inbox.ErrValidation) {
		t.Errorf("error = %v, want ErrValidation", err)
	}
}

// --- HTTP layer ---

func TestSnoozeEndpointRoundTrip(t *testing.T) {
	f := newSnoozeFixture(t)
	// Relative to the fixture's fixed clock — the Service validates against it.
	until := f.now.Add(24 * time.Hour)
	body := `{"snooze_until":"` + until.Format(time.RFC3339) + `"}`

	res := serve(t, f.handler, http.MethodPut, "/inbox/threads/"+f.thread.ID.String()+"/snooze", body)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", res.Code, res.Body.String())
	}
	var got struct {
		ThreadID    string `json:"thread_id"`
		SnoozeUntil string `json:"snooze_until"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ThreadID != f.thread.ID.String() {
		t.Errorf("thread_id = %s, want %s", got.ThreadID, f.thread.ID)
	}
	// Echoed back so the client renders the instant the SERVER stored, rather
	// than assuming its own value survived unchanged.
	if got.SnoozeUntil != until.Format(time.RFC3339) {
		t.Errorf("snooze_until = %s, want %s", got.SnoozeUntil, until.Format(time.RFC3339))
	}

	res = serve(t, f.handler, http.MethodDelete, "/inbox/threads/"+f.thread.ID.String()+"/snooze", "")
	if res.Code != http.StatusNoContent {
		t.Errorf("DELETE status = %d, want 204 (%s)", res.Code, res.Body.String())
	}
}

// A well-formed timestamp that is out of bounds is 422, not 400 — so a client
// can tell "malformed request" from "moment rejected" on status alone.
func TestSnoozeEndpointStatusCodes(t *testing.T) {
	// Bodies are built from the fixture's fixed clock rather than hardcoded
	// years, so "beyond the horizon" stays beyond it forever.
	f0 := newSnoozeFixture(t)
	at := func(d time.Duration) string {
		return `{"snooze_until":"` + f0.now.Add(d).Format(time.RFC3339) + `"}`
	}
	tests := []struct {
		name string
		body string
		want int
	}{
		{"malformed json", `{`, http.StatusBadRequest},
		{"unknown field", `{"snooze_until":"` + f0.now.Add(time.Hour).Format(time.RFC3339) + `","nope":1}`, http.StatusBadRequest},
		{"not a timestamp", `{"snooze_until":"tomorrow"}`, http.StatusBadRequest},
		{"missing field", `{}`, http.StatusBadRequest},
		{"in the past", at(-time.Hour), http.StatusUnprocessableEntity},
		{"beyond the horizon", at(inbox.SnoozeMaxHorizon + time.Hour), http.StatusUnprocessableEntity},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newSnoozeFixture(t)
			res := serve(t, f.handler, http.MethodPut,
				"/inbox/threads/"+f.thread.ID.String()+"/snooze", tc.body)
			if res.Code != tc.want {
				t.Errorf("status = %d, want %d (%s)", res.Code, tc.want, res.Body.String())
			}
		})
	}
}

func TestSnoozeEndpointRecordsWhoDidIt(t *testing.T) {
	f := newSnoozeFixture(t)
	// Relative to the fixture's FIXED clock, not the wall clock: the Service
	// validates against the former, so a wall-clock instant would be "in the
	// past" whenever the two disagree.
	until := f.now.Add(time.Hour)
	res := serve(t, f.handler, http.MethodPut, "/inbox/threads/"+f.thread.ID.String()+"/snooze",
		`{"snooze_until":"`+until.Format(time.RFC3339)+`"}`)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", res.Code, res.Body.String())
	}
	stored := f.snoozes.snoozes[f.thread.ID]
	if stored.SnoozedBy == nil {
		t.Error("snoozed_by was not recorded for a session principal")
	}
}

func TestListThreadsSnoozedScopeMapsToFilter(t *testing.T) {
	f := newSnoozeFixture(t)
	res := serve(t, f.handler, http.MethodGet, "/inbox/threads?scope=snoozed", "")
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", res.Code, res.Body.String())
	}
	got := f.threads.lastListFilter
	if !got.SnoozedOnly {
		t.Error("scope=snoozed did not set SnoozedOnly")
	}
	if got.SnoozeHidden {
		t.Error("scope=snoozed also set SnoozeHidden, which can only match nothing")
	}
}

// Every ordinary scope hides snoozed threads — that is what snoozing means.
func TestOrdinaryScopesHideSnoozedThreads(t *testing.T) {
	for _, scope := range []string{"", "all", "unread", "today", "this_week", "awaiting_reply"} {
		t.Run("scope="+scope, func(t *testing.T) {
			f := newSnoozeFixture(t)
			res := serve(t, f.handler, http.MethodGet, "/inbox/threads?scope="+scope, "")
			if res.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (%s)", res.Code, res.Body.String())
			}
			if !f.threads.lastListFilter.SnoozeHidden {
				t.Error("snoozed threads were not hidden")
			}
		})
	}
}

// ...except a search. Answering "no results" for a thread the operator
// snoozed last week reads as data loss, not as triage.
func TestSearchStillFindsSnoozedThreads(t *testing.T) {
	f := newSnoozeFixture(t)
	res := serve(t, f.handler, http.MethodGet, "/inbox/threads?q=invoice", "")
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", res.Code, res.Body.String())
	}
	got := f.threads.lastListFilter
	if got.SnoozeHidden {
		t.Error("a search hid snoozed threads; it must be able to find them")
	}
	if got.SnoozedOnly {
		t.Error("a search restricted to snoozed threads only")
	}
}
