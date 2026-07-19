package campaign

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

type fakeStore struct{}

func (fakeStore) Create(_ context.Context, _ uuid.UUID, in CreateInput) (gen.Campaign, error) {
	return gen.Campaign{ID: uuid.New(), Name: in.Name, Subject: in.Subject}, nil
}
func (fakeStore) Get(context.Context, uuid.UUID, uuid.UUID) (gen.Campaign, error) {
	return gen.Campaign{}, nil
}
func (fakeStore) List(context.Context, uuid.UUID) ([]gen.Campaign, error) { return nil, nil }
func (fakeStore) Stats(context.Context, uuid.UUID) (map[string]int64, error) {
	return nil, nil
}

type okChecker struct{ active bool }

func (o okChecker) MailboxActive(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return o.active, nil
}
func (o okChecker) ListExists(context.Context, uuid.UUID, uuid.UUID) (bool, error) { return true, nil }

func TestCreateRejectsInactiveMailbox(t *testing.T) {
	svc := NewService(fakeStore{}, okChecker{active: false})
	_, err := svc.Create(context.Background(), uuid.New(), CreateInput{
		Name: "Q3", Subject: "Hi", BodyText: "hello", MailboxID: uuid.New(), ListID: uuid.New(),
	})
	if err != ErrMailboxNotActive {
		t.Fatalf("expected ErrMailboxNotActive, got %v", err)
	}
}

func TestCreateSucceeds(t *testing.T) {
	svc := NewService(fakeStore{}, okChecker{active: true})
	c, err := svc.Create(context.Background(), uuid.New(), CreateInput{
		Name: "Q3", Subject: "Hi", BodyText: "hello", MailboxID: uuid.New(), ListID: uuid.New(),
	})
	if err != nil || c.Name != "Q3" {
		t.Fatalf("Create: %v %+v", err, c)
	}
}
