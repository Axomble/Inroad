package campaign

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

type fakeStore struct {
	status    string
	sendIDs   []uuid.UUID
	setStatus CampaignStatus
}

func (*fakeStore) Create(_ context.Context, _ uuid.UUID, in CreateInput) (gen.Campaign, error) {
	return gen.Campaign{ID: uuid.New(), Name: in.Name, Subject: in.Subject}, nil
}
func (f *fakeStore) Get(context.Context, uuid.UUID, uuid.UUID) (gen.Campaign, error) {
	return gen.Campaign{Status: f.status}, nil
}
func (*fakeStore) List(context.Context, uuid.UUID) ([]gen.Campaign, error) { return nil, nil }
func (*fakeStore) Stats(context.Context, uuid.UUID) (map[string]int64, error) {
	return nil, nil
}
func (f *fakeStore) EnqueueSends(context.Context, uuid.UUID, uuid.UUID) ([]uuid.UUID, error) {
	return f.sendIDs, nil
}
func (f *fakeStore) SetStatus(_ context.Context, _, _ uuid.UUID, status CampaignStatus) error {
	f.setStatus = status
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
	n, err := svc.Launch(context.Background(), uuid.New(), uuid.New(), enq)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if n != len(ids) {
		t.Fatalf("queued: got %d want %d", n, len(ids))
	}
	if len(enq.enqueued) != len(ids) {
		t.Fatalf("enqueued: got %d want %d", len(enq.enqueued), len(ids))
	}
	if store.setStatus != StatusRunning {
		t.Fatalf("status: got %q want %q", store.setStatus, StatusRunning)
	}
}
