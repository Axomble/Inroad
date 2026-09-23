package campaign

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/audit"
)

type captureRecorder struct{ events []audit.Event }

func (c *captureRecorder) Record(_ context.Context, ev audit.Event) error {
	c.events = append(c.events, ev)
	return nil
}

func TestLaunchPauseResumeAreAudited(t *testing.T) {
	store := &fakeStore{status: string(StatusDraft), steps: 1, enrollments: []Enrollment{{ID: uuid.New()}, {ID: uuid.New()}}}
	rec := &captureRecorder{}
	svc := NewService(store, okChecker{active: true}, WithAudit(rec))
	ws, id, uid := uuid.New(), uuid.New(), uuid.New()
	ctx := audit.WithActor(context.Background(), audit.UserActor(uid))

	if _, err := svc.Launch(ctx, ws, id, &fakeEnqueuer{}); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	store.status = string(StatusRunning)
	if err := svc.Pause(ctx, ws, id); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	store.status = string(StatusPaused)
	if err := svc.Resume(ctx, ws, id); err != nil {
		t.Fatalf("Resume: %v", err)
	}

	want := []audit.Action{audit.ActionCampaignStarted, audit.ActionCampaignPaused, audit.ActionCampaignResumed}
	if len(rec.events) != len(want) {
		t.Fatalf("recorded %d events, want %d", len(rec.events), len(want))
	}
	for i, ev := range rec.events {
		if ev.Action != want[i] || ev.WorkspaceID != ws || ev.TargetType != "campaign" || ev.Actor.ID != uid.String() {
			t.Fatalf("event %d = %+v", i, ev)
		}
		if err := audit.Validate(ev); err != nil {
			t.Fatalf("event %d would be refused at write: %v", i, err)
		}
	}
	if rec.events[0].Metadata["enrolled"] != "2" {
		t.Fatalf("started metadata = %v, want enrolled=2", rec.events[0].Metadata)
	}
}

func TestRejectedTransitionsAreNotAudited(t *testing.T) {
	store := &fakeStore{status: string(StatusDraft), steps: 1}
	rec := &captureRecorder{}
	svc := NewService(store, okChecker{active: true}, WithAudit(rec))
	ws, id := uuid.New(), uuid.New()

	if err := svc.Pause(context.Background(), ws, id); err == nil {
		t.Fatal("paused a draft")
	}
	if _, err := svc.Launch(context.Background(), ws, id, &fakeEnqueuer{}); err == nil {
		t.Fatal("launched onto an empty list")
	}
	if len(rec.events) != 0 {
		t.Fatalf("recorded %d events for transitions that did not happen", len(rec.events))
	}
}
