package deadletter

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// fakeStore is an in-memory Store that reproduces the ONE behaviour the service
// depends on for correctness: the status-guarded claim. ClaimReplay flips
// pending->replayed under a mutex and reports claimed=false for anything not
// pending, exactly as the SQL predicate does. A fake that always claimed would
// assert nothing about double-send, which is the whole point of this domain.
type fakeStore struct {
	mu   sync.Mutex
	rows map[uuid.UUID]gen.TaskDeadLetter

	// Injected failures, for the error branches.
	claimErr   error
	getErr     error
	listErr    error
	insertErr  error
	releaseErr error

	// Observed calls.
	releases int
	inserted []Capture
}

func newFakeStore() *fakeStore {
	return &fakeStore{rows: map[uuid.UUID]gen.TaskDeadLetter{}}
}

// seed inserts a row directly, bypassing Insert, so a test can set up a
// non-pending row without going through the service.
func (f *fakeStore) seed(row gen.TaskDeadLetter) gen.TaskDeadLetter {
	f.mu.Lock()
	defer f.mu.Unlock()
	if row.ID == uuid.Nil {
		row.ID = uuid.New()
	}
	if row.Status == "" {
		row.Status = StatusPending
	}
	f.rows[row.ID] = row
	return row
}

func (f *fakeStore) Insert(_ context.Context, in Capture) (gen.TaskDeadLetter, error) {
	if f.insertErr != nil {
		return gen.TaskDeadLetter{}, f.insertErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inserted = append(f.inserted, in)
	row := gen.TaskDeadLetter{
		ID: uuid.New(), WorkspaceID: in.WorkspaceID, TaskType: in.TaskType,
		Payload: in.Payload, LastError: in.LastError, AttemptCount: in.AttemptCount,
		Status: StatusPending,
	}
	f.rows[row.ID] = row
	return row, nil
}

func (f *fakeStore) List(_ context.Context, ws uuid.UUID, status string, limit, offset int32) ([]gen.TaskDeadLetter, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []gen.TaskDeadLetter
	for _, row := range f.rows {
		// The workspace pin is the fake's job to honour too: a fake that
		// ignored it would let a tenant-isolation test pass vacuously.
		if row.WorkspaceID != ws {
			continue
		}
		if status != "" && row.Status != status {
			continue
		}
		out = append(out, row)
	}
	if int(offset) >= len(out) {
		return nil, nil
	}
	out = out[offset:]
	if int(limit) < len(out) {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeStore) Get(_ context.Context, ws, id uuid.UUID) (gen.TaskDeadLetter, bool, error) {
	if f.getErr != nil {
		return gen.TaskDeadLetter{}, false, f.getErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[id]
	if !ok || row.WorkspaceID != ws {
		return gen.TaskDeadLetter{}, false, nil
	}
	return row, true, nil
}

func (f *fakeStore) ClaimReplay(_ context.Context, ws, id uuid.UUID) (gen.TaskDeadLetter, bool, error) {
	if f.claimErr != nil {
		return gen.TaskDeadLetter{}, false, f.claimErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[id]
	if !ok || row.WorkspaceID != ws || row.Status != StatusPending {
		return gen.TaskDeadLetter{}, false, nil
	}
	row.Status = StatusReplayed
	row.ReplayedAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
	f.rows[id] = row
	return row, true, nil
}

func (f *fakeStore) ReleaseReplay(_ context.Context, ws, id uuid.UUID, replayedAt pgtype.Timestamptz) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releases++
	if f.releaseErr != nil {
		return f.releaseErr
	}
	row, ok := f.rows[id]
	if !ok || row.WorkspaceID != ws || row.Status != StatusReplayed || !row.ReplayedAt.Time.Equal(replayedAt.Time) {
		return nil
	}
	row.Status = StatusPending
	row.ReplayedAt = pgtype.Timestamptz{}
	f.rows[id] = row
	return nil
}

func (f *fakeStore) Discard(_ context.Context, ws, id uuid.UUID) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[id]
	if !ok || row.WorkspaceID != ws || row.Status != StatusPending {
		return false, nil
	}
	row.Status = StatusDiscarded
	f.rows[id] = row
	return true, nil
}

func (f *fakeStore) status(id uuid.UUID) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rows[id].Status
}

// fakeEnqueuer counts enqueues so a test can assert "exactly once" directly,
// which is the property that matters more than any other here.
type fakeEnqueuer struct {
	mu    sync.Mutex
	calls []enqueueCall
	err   error
}

type enqueueCall struct {
	taskType string
	payload  []byte
	key      string
}

func (f *fakeEnqueuer) EnqueueReplay(_ context.Context, taskType string, payload []byte, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.calls = append(f.calls, enqueueCall{taskType: taskType, payload: payload, key: key})
	return nil
}

func (f *fakeEnqueuer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// payloadFor builds a well-formed task payload naming ws, the shape every
// capturable payload in internal/platform/queue has.
func payloadFor(t *testing.T, ws uuid.UUID) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]string{
		"enrollment_id": uuid.New().String(),
		"workspace_id":  ws.String(),
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return b
}

// seedPending stores one replayable dead letter and returns it with its store
// and service, which is the setup nearly every test below needs.
func seedPending(t *testing.T) (*fakeStore, *fakeEnqueuer, *Service, uuid.UUID, gen.TaskDeadLetter) {
	t.Helper()
	store := newFakeStore()
	enq := &fakeEnqueuer{}
	ws := uuid.New()
	row := store.seed(gen.TaskDeadLetter{
		WorkspaceID: ws, TaskType: "sequence:advance",
		Payload: payloadFor(t, ws), Status: StatusPending, AttemptCount: 5,
	})
	return store, enq, NewService(store, enq), ws, row
}

// The happy path: a pending row replays, the ORIGINAL payload is what goes back
// on the queue, and the row becomes 'replayed'.
func TestReplayEnqueuesTheOriginalPayloadAndMarksTheRow(t *testing.T) {
	store, enq, svc, ws, row := seedPending(t)

	got, err := svc.Replay(context.Background(), ws, row.ID)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if got.Status != StatusReplayed {
		t.Errorf("returned status = %q, want %q", got.Status, StatusReplayed)
	}
	if store.status(row.ID) != StatusReplayed {
		t.Errorf("stored status = %q, want %q", store.status(row.ID), StatusReplayed)
	}
	if enq.count() != 1 {
		t.Fatalf("enqueued %d times, want exactly 1", enq.count())
	}
	call := enq.calls[0]
	if call.taskType != "sequence:advance" {
		t.Errorf("task type = %q, want sequence:advance", call.taskType)
	}
	if string(call.payload) != string(row.Payload) {
		t.Errorf("payload = %s, want the original %s", call.payload, row.Payload)
	}
	if call.key != replayKey(row.ID) {
		t.Errorf("key = %q, want %q", call.key, replayKey(row.ID))
	}
}

// THE double-send test. Replaying an already-replayed row must not enqueue a
// second time: the status-guarded claim, not the queue key, is what stops it.
func TestReplayOfAnAlreadyReplayedRowDoesNotEnqueueAgain(t *testing.T) {
	store, enq, svc, ws, row := seedPending(t)

	if _, err := svc.Replay(context.Background(), ws, row.ID); err != nil {
		t.Fatalf("first Replay: %v", err)
	}

	_, err := svc.Replay(context.Background(), ws, row.ID)
	if !errors.Is(err, ErrNotPending) {
		t.Fatalf("second Replay error = %v, want ErrNotPending", err)
	}
	if enq.count() != 1 {
		t.Fatalf("enqueued %d times across two replays, want exactly 1", enq.count())
	}
	if store.status(row.ID) != StatusReplayed {
		t.Errorf("status = %q, want it left at %q", store.status(row.ID), StatusReplayed)
	}
}

// The same property under genuine concurrency: N callers racing the same row
// must produce exactly one enqueue and N-1 ErrNotPending.
func TestConcurrentReplaysEnqueueExactlyOnce(t *testing.T) {
	_, enq, svc, ws, row := seedPending(t)

	const racers = 8
	var wg sync.WaitGroup
	errs := make([]error, racers)
	start := make(chan struct{})
	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = svc.Replay(context.Background(), ws, row.ID)
		}()
	}
	close(start)
	wg.Wait()

	var succeeded, notPending int
	for _, err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrNotPending):
			notPending++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if succeeded != 1 {
		t.Errorf("%d racers succeeded, want exactly 1", succeeded)
	}
	if notPending != racers-1 {
		t.Errorf("%d racers got ErrNotPending, want %d", notPending, racers-1)
	}
	if enq.count() != 1 {
		t.Fatalf("enqueued %d times under %d concurrent replays, want exactly 1", enq.count(), racers)
	}
}

// A discarded row is terminal too: it must not be replayable.
func TestReplayOfADiscardedRowIsRefused(t *testing.T) {
	store, enq, svc, ws, _ := seedPending(t)
	discarded := store.seed(gen.TaskDeadLetter{
		WorkspaceID: ws, TaskType: "inbox:poll",
		Payload: payloadFor(t, ws), Status: StatusDiscarded,
	})

	_, err := svc.Replay(context.Background(), ws, discarded.ID)
	if !errors.Is(err, ErrNotPending) {
		t.Fatalf("Replay error = %v, want ErrNotPending", err)
	}
	if enq.count() != 0 {
		t.Errorf("enqueued %d times, want 0", enq.count())
	}
}

// Tenant isolation: a row belonging to another workspace is invisible, and the
// error must be ErrNotFound (not ErrNotPending), so a caller cannot probe for
// the existence of another tenant's ids.
func TestReplayCannotReachAnotherWorkspacesRow(t *testing.T) {
	store, enq, svc, ws, row := seedPending(t)
	intruder := uuid.New()

	_, err := svc.Replay(context.Background(), intruder, row.ID)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Replay error = %v, want ErrNotFound", err)
	}
	if enq.count() != 0 {
		t.Errorf("enqueued %d times for a foreign row, want 0", enq.count())
	}
	if store.status(row.ID) != StatusPending {
		t.Errorf("foreign replay changed status to %q; want it untouched at %q",
			store.status(row.ID), StatusPending)
	}
	_ = ws
}

// The same isolation on the read paths.
func TestGetAndListAreWorkspacePinned(t *testing.T) {
	store, enq, svc, ws, row := seedPending(t)
	intruder := uuid.New()
	store.seed(gen.TaskDeadLetter{
		WorkspaceID: intruder, TaskType: "inbox:poll",
		Payload: payloadFor(t, intruder), Status: StatusPending,
	})

	if _, err := svc.Get(context.Background(), intruder, row.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get across tenants error = %v, want ErrNotFound", err)
	}

	mine, err := svc.List(context.Background(), ws, ListParams{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(mine) != 1 {
		t.Fatalf("List returned %d rows, want only this workspace's 1", len(mine))
	}
	if mine[0].WorkspaceID != ws {
		t.Errorf("List leaked workspace %v", mine[0].WorkspaceID)
	}
	_ = enq
}

// Discard is workspace-pinned and status-guarded the same way replay is.
func TestDiscard(t *testing.T) {
	t.Run("files a pending row", func(t *testing.T) {
		store, _, svc, ws, row := seedPending(t)
		if err := svc.Discard(context.Background(), ws, row.ID); err != nil {
			t.Fatalf("Discard: %v", err)
		}
		if store.status(row.ID) != StatusDiscarded {
			t.Errorf("status = %q, want %q", store.status(row.ID), StatusDiscarded)
		}
	})

	t.Run("refuses an already-replayed row", func(t *testing.T) {
		store, _, svc, ws, row := seedPending(t)
		if _, err := svc.Replay(context.Background(), ws, row.ID); err != nil {
			t.Fatalf("Replay: %v", err)
		}
		if err := svc.Discard(context.Background(), ws, row.ID); !errors.Is(err, ErrNotPending) {
			t.Fatalf("Discard error = %v, want ErrNotPending", err)
		}
		if store.status(row.ID) != StatusReplayed {
			t.Errorf("status = %q, want it left at %q", store.status(row.ID), StatusReplayed)
		}
	})

	t.Run("cannot reach another workspace's row", func(t *testing.T) {
		store, _, svc, _, row := seedPending(t)
		if err := svc.Discard(context.Background(), uuid.New(), row.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Discard error = %v, want ErrNotFound", err)
		}
		if store.status(row.ID) != StatusPending {
			t.Errorf("status = %q, want untouched %q", store.status(row.ID), StatusPending)
		}
	})
}

// A malformed payload must be refused rather than enqueued, and the claim it
// already won must be released so the row stays actionable once fixed.
func TestReplayOfAMalformedPayload(t *testing.T) {
	ws := uuid.New()
	other := uuid.New()
	cases := []struct {
		name    string
		payload []byte
	}{
		{"not JSON at all", []byte("{definitely not json")},
		{"JSON but not an object", []byte(`"a bare string"`)},
		{"JSON null (the capture fallback for an empty payload)", []byte("null")},
		{"object with no workspace_id", []byte(`{"enrollment_id":"x"}`)},
		{"workspace_id that is not a UUID", []byte(`{"workspace_id":"not-a-uuid"}`)},
		{"workspace_id naming a DIFFERENT tenant", []byte(`{"workspace_id":"` + other.String() + `"}`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			enq := &fakeEnqueuer{}
			svc := NewService(store, enq)
			row := store.seed(gen.TaskDeadLetter{
				WorkspaceID: ws, TaskType: "sequence:advance",
				Payload: tc.payload, Status: StatusPending,
			})

			_, err := svc.Replay(context.Background(), ws, row.ID)
			if !errors.Is(err, ErrMalformedPayload) {
				t.Fatalf("Replay error = %v, want ErrMalformedPayload", err)
			}
			if enq.count() != 0 {
				t.Errorf("enqueued %d times for a malformed payload, want 0", enq.count())
			}
			if store.status(row.ID) != StatusPending {
				t.Errorf("status = %q, want the claim released back to %q",
					store.status(row.ID), StatusPending)
			}
		})
	}
}

// A queue that refuses the enqueue must not leave the row marked replayed —
// nothing reached the queue, so the row goes back to pending and the operator
// can retry.
func TestReplayReleasesTheClaimWhenTheEnqueueFails(t *testing.T) {
	store, enq, svc, ws, row := seedPending(t)
	enq.err = errors.New("redis is down")

	_, err := svc.Replay(context.Background(), ws, row.ID)
	if err == nil {
		t.Fatal("Replay succeeded despite a failing enqueue")
	}
	if !errors.Is(err, enq.err) {
		t.Errorf("error = %v, want it to wrap the enqueue failure", err)
	}
	if store.status(row.ID) != StatusPending {
		t.Errorf("status = %q, want released back to %q", store.status(row.ID), StatusPending)
	}

	// And once the queue recovers, the SAME row replays cleanly — the release
	// genuinely restored it rather than merely reporting so.
	enq.err = nil
	if _, err := svc.Replay(context.Background(), ws, row.ID); err != nil {
		t.Fatalf("Replay after recovery: %v", err)
	}
	if enq.count() != 1 {
		t.Errorf("enqueued %d times, want 1", enq.count())
	}
}

// A release that ITSELF fails must leave the row in 'replayed' — the safe
// direction. It must not be silently returned to pending, which would make the
// task replayable again on a path where the enqueue outcome is unknown.
func TestReplayLeavesTheRowClaimedWhenTheReleaseAlsoFails(t *testing.T) {
	store, enq, svc, ws, row := seedPending(t)
	enq.err = errors.New("redis is down")
	store.releaseErr = errors.New("database is down")

	if _, err := svc.Replay(context.Background(), ws, row.ID); err == nil {
		t.Fatal("Replay succeeded despite a failing enqueue")
	}
	if store.status(row.ID) != StatusReplayed {
		t.Errorf("status = %q, want it left claimed at %q", store.status(row.ID), StatusReplayed)
	}
}

// The claim query failing is a server error, distinct from losing the claim.
func TestReplaySurfacesAClaimError(t *testing.T) {
	store, enq, svc, ws, row := seedPending(t)
	store.claimErr = errors.New("connection reset")

	_, err := svc.Replay(context.Background(), ws, row.ID)
	if !errors.Is(err, store.claimErr) {
		t.Fatalf("error = %v, want it to wrap the claim failure", err)
	}
	if errors.Is(err, ErrNotPending) || errors.Is(err, ErrNotFound) {
		t.Error("a store failure was reported as a client error")
	}
	if enq.count() != 0 {
		t.Errorf("enqueued %d times, want 0", enq.count())
	}
}

// Capture validates at its boundary and normalises an empty payload rather than
// losing the record.
func TestCapture(t *testing.T) {
	ws := uuid.New()

	t.Run("records the task", func(t *testing.T) {
		store := newFakeStore()
		svc := NewService(store, &fakeEnqueuer{})
		row, err := svc.Capture(context.Background(), Capture{
			WorkspaceID: ws, TaskType: "inbox:poll",
			Payload: payloadFor(t, ws), LastError: "dial timeout", AttemptCount: 3,
		})
		if err != nil {
			t.Fatalf("Capture: %v", err)
		}
		if row.Status != StatusPending {
			t.Errorf("status = %q, want %q", row.Status, StatusPending)
		}
		if row.AttemptCount != 3 {
			t.Errorf("attempt count = %d, want 3", row.AttemptCount)
		}
	})

	t.Run("rejects a capture with no workspace", func(t *testing.T) {
		svc := NewService(newFakeStore(), &fakeEnqueuer{})
		_, err := svc.Capture(context.Background(), Capture{TaskType: "inbox:poll"})
		if !errors.Is(err, ErrValidation) {
			t.Fatalf("error = %v, want ErrValidation", err)
		}
	})

	t.Run("rejects a capture with no task type", func(t *testing.T) {
		svc := NewService(newFakeStore(), &fakeEnqueuer{})
		_, err := svc.Capture(context.Background(), Capture{WorkspaceID: ws})
		if !errors.Is(err, ErrValidation) {
			t.Fatalf("error = %v, want ErrValidation", err)
		}
	})

	t.Run("normalises an empty payload to JSON null", func(t *testing.T) {
		store := newFakeStore()
		svc := NewService(store, &fakeEnqueuer{})
		if _, err := svc.Capture(context.Background(), Capture{
			WorkspaceID: ws, TaskType: "inbox:poll",
		}); err != nil {
			t.Fatalf("Capture: %v", err)
		}
		if got := string(store.inserted[0].Payload); got != "null" {
			t.Errorf("payload = %q, want %q so the JSONB insert cannot fail", got, "null")
		}
	})

	t.Run("wraps a store failure", func(t *testing.T) {
		store := newFakeStore()
		store.insertErr = errors.New("disk full")
		svc := NewService(store, &fakeEnqueuer{})
		_, err := svc.Capture(context.Background(), Capture{
			WorkspaceID: ws, TaskType: "inbox:poll", Payload: payloadFor(t, ws),
		})
		if !errors.Is(err, store.insertErr) {
			t.Fatalf("error = %v, want it to wrap the store failure", err)
		}
	})
}

// List's boundary validation: an unknown status filter is a client error, and
// paging is clamped rather than trusted.
func TestListValidatesAndClampsItsInput(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store, &fakeEnqueuer{})
	ws := uuid.New()

	if _, err := svc.List(context.Background(), ws, ListParams{Status: "exploded"}); !errors.Is(err, ErrValidation) {
		t.Fatalf("unknown status error = %v, want ErrValidation", err)
	}
	for _, status := range []string{StatusPending, StatusReplayed, StatusDiscarded, ""} {
		if _, err := svc.List(context.Background(), ws, ListParams{Status: status}); err != nil {
			t.Errorf("List(status=%q): %v", status, err)
		}
	}

	// Seed more rows than maxLimit so the clamp is observable rather than
	// merely asserted on the params.
	for range maxLimit + 10 {
		store.seed(gen.TaskDeadLetter{
			WorkspaceID: ws, TaskType: "inbox:poll",
			Payload: payloadFor(t, ws), Status: StatusPending,
		})
	}
	rows, err := svc.List(context.Background(), ws, ListParams{Limit: 10_000, Offset: -5})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != maxLimit {
		t.Errorf("returned %d rows, want the clamp at %d", len(rows), maxLimit)
	}
}

// An empty workspace lists cleanly rather than erroring.
func TestListOfAnEmptyWorkspace(t *testing.T) {
	svc := NewService(newFakeStore(), &fakeEnqueuer{})
	rows, err := svc.List(context.Background(), uuid.New(), ListParams{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("returned %d rows, want 0", len(rows))
	}
}

// A store failure on List is propagated, not flattened into an empty page.
func TestListSurfacesAStoreFailure(t *testing.T) {
	store := newFakeStore()
	store.listErr = errors.New("connection reset")
	svc := NewService(store, &fakeEnqueuer{})
	if _, err := svc.List(context.Background(), uuid.New(), ListParams{}); !errors.Is(err, store.listErr) {
		t.Fatalf("error = %v, want it to wrap the store failure", err)
	}
}

// Losing the claim on a row that does not exist at all must report ErrNotFound,
// and a Get failure while explaining must degrade to ErrNotFound rather than
// leaking a store error onto a path where nothing happened.
func TestReplayOfAnUnknownIDReportsNotFound(t *testing.T) {
	store, enq, svc, ws, _ := seedPending(t)

	if _, err := svc.Replay(context.Background(), ws, uuid.New()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
	store.getErr = errors.New("connection reset")
	if _, err := svc.Replay(context.Background(), ws, uuid.New()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("error with a failing Get = %v, want ErrNotFound", err)
	}
	if enq.count() != 0 {
		t.Errorf("enqueued %d times, want 0", enq.count())
	}
}

// The replay key is derived from the row id alone, so every attempt at the same
// dead letter produces the same key — the queue-level half of the double-send
// defense. It must depend on the id and on nothing else (no clock, no counter),
// which is what makes a retried replay collapse rather than enqueue twice.
func TestReplayKeyIsStablePerRow(t *testing.T) {
	ids := make([]uuid.UUID, 4)
	for i := range ids {
		ids[i] = uuid.New()
	}

	// Same id, keys computed at different moments: identical.
	seen := map[uuid.UUID]string{}
	for _, id := range ids {
		seen[id] = replayKey(id)
	}
	for _, id := range ids {
		if got := replayKey(id); got != seen[id] {
			t.Errorf("replayKey(%v) changed between calls: %q then %q", id, seen[id], got)
		}
	}

	// Distinct ids never collide, or one row's replay would suppress another's.
	keys := map[string]uuid.UUID{}
	for id, key := range seen {
		if other, clash := keys[key]; clash {
			t.Errorf("rows %v and %v share the replay key %q", other, id, key)
		}
		keys[key] = id
	}
}
