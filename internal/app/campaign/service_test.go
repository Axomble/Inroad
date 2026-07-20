package campaign

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

type fakeStore struct {
	status  string
	sendIDs []uuid.UUID
	// launchCalled is set to true when LaunchTx runs so tests can assert
	// the tx path is actually exercised (not the removed EnqueueSends+SetStatus
	// two-step).
	launchCalled bool
}

func (*fakeStore) Create(_ context.Context, _ uuid.UUID, in CreateInput) (gen.Campaign, error) {
	return gen.Campaign{ID: uuid.New(), Name: in.Name, Subject: in.Subject}, nil
}
func (f *fakeStore) Get(context.Context, uuid.UUID, uuid.UUID) (gen.Campaign, error) {
	return gen.Campaign{Status: f.status}, nil
}
func (*fakeStore) List(context.Context, uuid.UUID) ([]gen.Campaign, error) { return nil, nil }
func (*fakeStore) Stats(context.Context, uuid.UUID, uuid.UUID) (map[string]int64, error) {
	return nil, nil
}
func (f *fakeStore) LaunchTx(context.Context, uuid.UUID, uuid.UUID) ([]uuid.UUID, error) {
	f.launchCalled = true
	return f.sendIDs, nil
}

// selectiveEnqueuer succeeds on any id it hasn't been told to fail. Used to
// prove the service tallies partial-enqueue failures rather than swallowing
// them.
type selectiveEnqueuer struct {
	fail     map[string]bool
	enqueued []string
}

func (s *selectiveEnqueuer) EnqueueSend(sendID string) error {
	if s.fail[sendID] {
		return errors.New("redis unavailable")
	}
	s.enqueued = append(s.enqueued, sendID)
	return nil
}

type fakeEnqueuer struct{ enqueued []string }

func (f *fakeEnqueuer) EnqueueSend(sendID string) error {
	f.enqueued = append(f.enqueued, sendID)
	return nil
}

type okChecker struct{ active bool }

func (o okChecker) MailboxActive(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return o.active, nil
}
func (o okChecker) ListExists(context.Context, uuid.UUID, uuid.UUID) (bool, error) { return true, nil }

func TestCreateRejectsInactiveMailbox(t *testing.T) {
	svc := NewService(&fakeStore{}, okChecker{active: false})
	_, err := svc.Create(context.Background(), uuid.New(), CreateInput{
		Name: "Q3", Subject: "Hi", BodyText: "hello", MailboxID: uuid.New(), ListID: uuid.New(),
	})
	if err != ErrMailboxNotActive {
		t.Fatalf("expected ErrMailboxNotActive, got %v", err)
	}
}

func TestCreateSucceeds(t *testing.T) {
	svc := NewService(&fakeStore{}, okChecker{active: true})
	c, err := svc.Create(context.Background(), uuid.New(), CreateInput{
		Name: "Q3", Subject: "Hi", BodyText: "hello", MailboxID: uuid.New(), ListID: uuid.New(),
	})
	if err != nil || c.Name != "Q3" {
		t.Fatalf("Create: %v %+v", err, c)
	}
}

func TestLaunchRejectsAlreadyLaunched(t *testing.T) {
	svc := NewService(&fakeStore{status: string(StatusRunning)}, okChecker{active: true})
	_, err := svc.Launch(context.Background(), uuid.New(), uuid.New(), &fakeEnqueuer{})
	if err != ErrAlreadyLaunched {
		t.Fatalf("expected ErrAlreadyLaunched, got %v", err)
	}
}

func TestLaunchRejectsEmptyList(t *testing.T) {
	svc := NewService(&fakeStore{status: string(StatusDraft)}, okChecker{active: true})
	_, err := svc.Launch(context.Background(), uuid.New(), uuid.New(), &fakeEnqueuer{})
	if err != ErrEmptyList {
		t.Fatalf("expected ErrEmptyList, got %v", err)
	}
}

func TestLaunchSucceeds(t *testing.T) {
	ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	store := &fakeStore{status: string(StatusDraft), sendIDs: ids}
	enq := &fakeEnqueuer{}
	svc := NewService(store, okChecker{active: true})
	res, err := svc.Launch(context.Background(), uuid.New(), uuid.New(), enq)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if res.EnqueuedCount != len(ids) {
		t.Fatalf("queued: got %d want %d", res.EnqueuedCount, len(ids))
	}
	if res.TotalSends != len(ids) {
		t.Fatalf("total sends: got %d want %d", res.TotalSends, len(ids))
	}
	if res.FailedEnqueueCount != 0 {
		t.Fatalf("expected no failed enqueues, got %d", res.FailedEnqueueCount)
	}
	if len(enq.enqueued) != len(ids) {
		t.Fatalf("enqueued: got %d want %d", len(enq.enqueued), len(ids))
	}
	if !store.launchCalled {
		t.Fatal("expected LaunchTx to be called")
	}
}

// TestLaunchCountsPartialEnqueueFailures proves the service no longer
// swallows enqueue errors - a redis blip that drops individual ids must show
// up in FailedEnqueueCount, so callers can log/alert and the stuck-send
// sweeper knows there's work to reconcile.
func TestLaunchCountsPartialEnqueueFailures(t *testing.T) {
	ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	store := &fakeStore{status: string(StatusDraft), sendIDs: ids}
	enq := &selectiveEnqueuer{fail: map[string]bool{ids[1].String(): true}}
	svc := NewService(store, okChecker{active: true})

	res, err := svc.Launch(context.Background(), uuid.New(), uuid.New(), enq)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if res.TotalSends != 3 || res.EnqueuedCount != 2 || res.FailedEnqueueCount != 1 {
		t.Fatalf("counts wrong: %+v", res)
	}
}
