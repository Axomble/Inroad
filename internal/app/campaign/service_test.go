package campaign

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

type fakeStore struct {
	status  string
	sendIDs []uuid.UUID // enrollment ids returned by EnrollTx
	steps   int64       // CountSteps result
	// enrollCalled is set to true when EnrollTx runs so tests can assert the
	// tx path is actually exercised.
	enrollCalled bool
	// campaigns keyed by (workspaceID, campaignID). Used by the cross-tenant
	// test to prove Get returns ErrNotFound for a campaign in another workspace.
	campaigns map[[2]uuid.UUID]gen.Campaign
	// detail-view fixtures.
	stepList     []gen.SequenceStep
	enrollCounts map[string]int64
}

func (*fakeStore) Create(_ context.Context, _ uuid.UUID, in CreateInput) (gen.Campaign, error) {
	return gen.Campaign{ID: uuid.New(), Name: in.Name, Subject: in.Subject}, nil
}
func (f *fakeStore) Get(_ context.Context, ws, id uuid.UUID) (gen.Campaign, error) {
	if f.campaigns != nil {
		c, ok := f.campaigns[[2]uuid.UUID{ws, id}]
		if !ok {
			return gen.Campaign{}, errNotFound
		}
		return c, nil
	}
	return gen.Campaign{Status: f.status}, nil
}
func (*fakeStore) List(context.Context, uuid.UUID) ([]gen.Campaign, error) { return nil, nil }
func (*fakeStore) Stats(context.Context, uuid.UUID, uuid.UUID) (map[string]int64, error) {
	return nil, nil
}
func (f *fakeStore) CountSteps(context.Context, uuid.UUID, uuid.UUID) (int64, error) {
	return f.steps, nil
}
func (f *fakeStore) EnrollTx(context.Context, uuid.UUID, uuid.UUID) ([]uuid.UUID, error) {
	f.enrollCalled = true
	return f.sendIDs, nil
}
func (*fakeStore) Reschedule(context.Context, uuid.UUID, uuid.UUID, time.Time) error { return nil }
func (f *fakeStore) ListSteps(context.Context, uuid.UUID, uuid.UUID) ([]gen.SequenceStep, error) {
	return f.stepList, nil
}
func (f *fakeStore) EnrollmentCounts(context.Context, uuid.UUID, uuid.UUID) (map[string]int64, error) {
	return f.enrollCounts, nil
}

// errNotFound is what the sqlc-backed Get returns when the row isn't in the
// caller's workspace (pgx.ErrNoRows). The fake stands in with a sentinel so
// tests don't have to import pgx.
var errNotFound = errors.New("no rows")

// selectiveEnqueuer succeeds on any id it hasn't been told to fail. Used to
// prove the service tallies partial-enqueue failures rather than swallowing
// them.
type selectiveEnqueuer struct {
	fail     map[string]bool
	enqueued []string
}

func (s *selectiveEnqueuer) EnqueueAdvanceAt(enrollmentID, _ string, _ time.Time) error {
	if s.fail[enrollmentID] {
		return errors.New("redis unavailable")
	}
	s.enqueued = append(s.enqueued, enrollmentID)
	return nil
}

type fakeEnqueuer struct{ enqueued []string }

func (f *fakeEnqueuer) EnqueueAdvanceAt(enrollmentID, _ string, _ time.Time) error {
	f.enqueued = append(f.enqueued, enrollmentID)
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

func TestLaunchRejectsNoSteps(t *testing.T) {
	// A draft campaign with a non-empty list but zero steps can't launch.
	svc := NewService(&fakeStore{status: string(StatusDraft), steps: 0}, okChecker{active: true})
	_, err := svc.Launch(context.Background(), uuid.New(), uuid.New(), &fakeEnqueuer{})
	if err != ErrNoSteps {
		t.Fatalf("expected ErrNoSteps, got %v", err)
	}
}

func TestLaunchRejectsEmptyList(t *testing.T) {
	// Steps exist, but EnrollTx returns no enrollments (empty list).
	svc := NewService(&fakeStore{status: string(StatusDraft), steps: 1}, okChecker{active: true})
	_, err := svc.Launch(context.Background(), uuid.New(), uuid.New(), &fakeEnqueuer{})
	if err != ErrEmptyList {
		t.Fatalf("expected ErrEmptyList, got %v", err)
	}
}

func TestLaunchSucceeds(t *testing.T) {
	ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	store := &fakeStore{status: string(StatusDraft), steps: 2, sendIDs: ids}
	enq := &fakeEnqueuer{}
	svc := NewService(store, okChecker{active: true})
	res, err := svc.Launch(context.Background(), uuid.New(), uuid.New(), enq)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if res.EnqueuedCount != len(ids) {
		t.Fatalf("queued: got %d want %d", res.EnqueuedCount, len(ids))
	}
	if res.TotalEnrolled != len(ids) {
		t.Fatalf("total enrolled: got %d want %d", res.TotalEnrolled, len(ids))
	}
	if res.FailedEnqueueCount != 0 {
		t.Fatalf("expected no failed enqueues, got %d", res.FailedEnqueueCount)
	}
	if len(enq.enqueued) != len(ids) {
		t.Fatalf("enqueued: got %d want %d", len(enq.enqueued), len(ids))
	}
	if !store.enrollCalled {
		t.Fatal("expected EnrollTx to be called")
	}
}

// TestLaunchCountsPartialEnqueueFailures proves the service no longer
// swallows enqueue errors - a redis blip that drops individual ids must show
// up in FailedEnqueueCount, so callers can log/alert and the stuck-send
// sweeper knows there's work to reconcile.
func TestLaunchCountsPartialEnqueueFailures(t *testing.T) {
	ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	store := &fakeStore{status: string(StatusDraft), steps: 1, sendIDs: ids}
	enq := &selectiveEnqueuer{fail: map[string]bool{ids[1].String(): true}}
	svc := NewService(store, okChecker{active: true})

	res, err := svc.Launch(context.Background(), uuid.New(), uuid.New(), enq)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if res.TotalEnrolled != 3 || res.EnqueuedCount != 2 || res.FailedEnqueueCount != 1 {
		t.Fatalf("counts wrong: %+v", res)
	}
}

func TestDetailIncludesStepsAndEnrollmentCounts(t *testing.T) {
	ws, id := uuid.New(), uuid.New()
	store := &fakeStore{
		campaigns:    map[[2]uuid.UUID]gen.Campaign{{ws, id}: {ID: id, WorkspaceID: ws, Name: "Q3", Status: "running"}},
		stepList:     []gen.SequenceStep{{StepOrder: 1}, {StepOrder: 2}},
		enrollCounts: map[string]int64{"active": 5, "completed": 1},
	}
	svc := NewService(store, okChecker{active: true})
	d, err := svc.Detail(context.Background(), ws, id)
	if err != nil {
		t.Fatalf("Detail: %v", err)
	}
	if len(d.Steps) != 2 {
		t.Fatalf("want 2 steps, got %d", len(d.Steps))
	}
	if d.Enrollments["active"] != 5 || d.Enrollments["completed"] != 1 {
		t.Fatalf("enrollment counts wrong: %+v", d.Enrollments)
	}
}

func TestDetailCrossTenantIsNotFound(t *testing.T) {
	store := &fakeStore{campaigns: map[[2]uuid.UUID]gen.Campaign{
		{uuid.New(), uuid.New()}: {Name: "foreign"},
	}}
	svc := NewService(store, okChecker{active: true})
	if _, err := svc.Detail(context.Background(), uuid.New(), uuid.New()); err != ErrNotFound {
		t.Fatalf("want ErrNotFound for cross-tenant detail, got %v", err)
	}
}

// TestCrossTenantGetReturnsNotFound guards defense-in-depth on the read
// path: Get is workspace-scoped at the SQL layer (see queries/campaign.sql
// "WHERE id = $1 AND workspace_id = $2"), so a caller supplying a campaign
// id that belongs to a different tenant must see "not found", not another
// tenant's campaign row.
func TestCrossTenantGetReturnsNotFound(t *testing.T) {
	otherWS := uuid.New()
	callerWS := uuid.New()
	campaignID := uuid.New()

	store := &fakeStore{
		campaigns: map[[2]uuid.UUID]gen.Campaign{
			{otherWS, campaignID}: {ID: campaignID, WorkspaceID: otherWS, Name: "foreign"},
		},
	}
	svc := NewService(store, okChecker{active: true})

	if _, err := svc.Get(context.Background(), callerWS, campaignID); err != errNotFound {
		t.Fatalf("expected cross-tenant Get to fail with not-found, got %v", err)
	}
}
