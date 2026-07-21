package sequence

import (
	"context"
	"testing"
	"time"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/queue"
)

type sweepCore struct {
	coreapi.Client
	due []coreapi.DueEnrollment
	err error
}

func (s *sweepCore) ListDueEnrollments(context.Context) ([]coreapi.DueEnrollment, error) {
	return s.due, s.err
}

type countEnq struct{ ids []string }

func (c *countEnq) EnqueueAdvanceAt(string, string, time.Time) error { return nil }
func (c *countEnq) EnqueueAdvanceIn(id, _ string, _ time.Duration) error {
	c.ids = append(c.ids, id)
	return nil
}

func sweepTask() *asynq.Task { return asynq.NewTask(queue.TaskSweepEnrollments, nil) }

func TestSweepReenqueuesDueEnrollments(t *testing.T) {
	core := &sweepCore{due: []coreapi.DueEnrollment{
		{EnrollmentID: "e1", WorkspaceID: "w"}, {EnrollmentID: "e2", WorkspaceID: "w"},
	}}
	enq := &countEnq{}
	if err := SweepHandler(core, enq)(context.Background(), sweepTask()); err != nil {
		t.Fatal(err)
	}
	if len(enq.ids) != 2 {
		t.Fatalf("want 2 re-enqueued, got %d", len(enq.ids))
	}
}

func TestSweepNoDueIsNoOp(t *testing.T) {
	enq := &countEnq{}
	if err := SweepHandler(&sweepCore{}, enq)(context.Background(), sweepTask()); err != nil {
		t.Fatal(err)
	}
	if len(enq.ids) != 0 {
		t.Fatalf("expected no re-enqueues, got %d", len(enq.ids))
	}
}
