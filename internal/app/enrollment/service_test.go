package enrollment

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

type fakeStore struct {
	advancedStep  int32
	advancedDue   time.Time
	completedStep int32
	completed     bool
	stoppedReason StopReason
	threadRoot    string
}

func (f *fakeStore) Enroll(context.Context, uuid.UUID, uuid.UUID) ([]uuid.UUID, error) {
	return []uuid.UUID{uuid.New(), uuid.New()}, nil
}
func (f *fakeStore) Get(context.Context, uuid.UUID, uuid.UUID) (gen.SequenceEnrollment, error) {
	return gen.SequenceEnrollment{}, nil
}
func (f *fakeStore) AdvanceStep(_ context.Context, _, _ uuid.UUID, step int32, due time.Time) error {
	f.advancedStep, f.advancedDue = step, due
	return nil
}
func (f *fakeStore) Complete(_ context.Context, _, _ uuid.UUID, step int32) error {
	f.completed, f.completedStep = true, step
	return nil
}
func (f *fakeStore) Stop(_ context.Context, _, _ uuid.UUID, r StopReason) error {
	f.stoppedReason = r
	return nil
}
func (f *fakeStore) SetDue(context.Context, uuid.UUID, uuid.UUID, time.Time) error { return nil }
func (f *fakeStore) SetThreadRoot(_ context.Context, _, _ uuid.UUID, mid string) error {
	f.threadRoot = mid
	return nil
}
func (f *fakeStore) CountByStatus(context.Context, uuid.UUID, uuid.UUID) (map[string]int64, error) {
	return map[string]int64{"active": 3}, nil
}

func TestMarkStepSentAdvancesMidSequence(t *testing.T) {
	f := &fakeStore{}
	svc := NewService(f)
	due := time.Now().Add(48 * time.Hour)
	if err := svc.MarkStepSent(context.Background(), uuid.New(), uuid.New(), 1, due, false, ""); err != nil {
		t.Fatal(err)
	}
	if f.completed {
		t.Fatal("mid-sequence step must not complete the enrollment")
	}
	if f.advancedStep != 1 || !f.advancedDue.Equal(due) {
		t.Fatalf("advance step=%d due=%v", f.advancedStep, f.advancedDue)
	}
}

func TestMarkStepSentCompletesOnLastStep(t *testing.T) {
	f := &fakeStore{}
	svc := NewService(f)
	if err := svc.MarkStepSent(context.Background(), uuid.New(), uuid.New(), 3, time.Time{}, true, ""); err != nil {
		t.Fatal(err)
	}
	if !f.completed || f.completedStep != 3 {
		t.Fatalf("expected complete at step 3, got completed=%v step=%d", f.completed, f.completedStep)
	}
}

func TestMarkStepSentRecordsThreadRootOnStep1(t *testing.T) {
	f := &fakeStore{}
	svc := NewService(f)
	if err := svc.MarkStepSent(context.Background(), uuid.New(), uuid.New(), 1, time.Now(), false, "<root@x>"); err != nil {
		t.Fatal(err)
	}
	if f.threadRoot != "<root@x>" {
		t.Fatalf("thread root not stored on step 1, got %q", f.threadRoot)
	}
}

func TestMarkStepSentSkipsThreadRootAfterStep1(t *testing.T) {
	f := &fakeStore{}
	svc := NewService(f)
	if err := svc.MarkStepSent(context.Background(), uuid.New(), uuid.New(), 2, time.Now(), false, "<later@x>"); err != nil {
		t.Fatal(err)
	}
	if f.threadRoot != "" {
		t.Fatalf("thread root must only be set on step 1, got %q", f.threadRoot)
	}
}

func TestMarkStepStoppedPassesReason(t *testing.T) {
	f := &fakeStore{}
	svc := NewService(f)
	if err := svc.MarkStepStopped(context.Background(), uuid.New(), uuid.New(), StopSuppressed); err != nil {
		t.Fatal(err)
	}
	if f.stoppedReason != StopSuppressed {
		t.Fatalf("want suppressed, got %s", f.stoppedReason)
	}
}
