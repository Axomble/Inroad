package campaign_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/campaign"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// fakeStore implements campaign.Store with just enough behaviour to drive the
// lifecycle methods under test (Pause/Resume/Rename/DeleteDraft). Every
// campaign lives in a workspace-scoped map, mirroring the white-box fake in
// service_test.go, so a wrong-workspace lookup misses with errNotFound rather
// than falling through to a shared zero-value campaign.
type fakeStore struct {
	campaigns map[[2]uuid.UUID]gen.Campaign

	// spies proving a rejected transition never reaches the store.
	statusCalls int
	lastStatus  campaign.CampaignStatus
	renameCalls int
	lastName    string
	deleteCalls int
}

var errNotFound = errors.New("no rows")

func (f *fakeStore) Get(_ context.Context, ws, id uuid.UUID) (gen.Campaign, error) {
	c, ok := f.campaigns[[2]uuid.UUID{ws, id}]
	if !ok {
		return gen.Campaign{}, errNotFound
	}
	return c, nil
}

func (f *fakeStore) SetStatus(_ context.Context, ws, id uuid.UUID, status campaign.CampaignStatus) error {
	f.statusCalls++
	f.lastStatus = status
	c, ok := f.campaigns[[2]uuid.UUID{ws, id}]
	if !ok {
		return errNotFound
	}
	c.Status = string(status)
	f.campaigns[[2]uuid.UUID{ws, id}] = c
	return nil
}

func (f *fakeStore) Rename(_ context.Context, ws, id uuid.UUID, name string) (gen.Campaign, error) {
	f.renameCalls++
	f.lastName = name
	c, ok := f.campaigns[[2]uuid.UUID{ws, id}]
	if !ok {
		return gen.Campaign{}, errNotFound
	}
	c.Name = name
	f.campaigns[[2]uuid.UUID{ws, id}] = c
	return c, nil
}

func (f *fakeStore) DeleteDraft(_ context.Context, ws, id uuid.UUID) error {
	f.deleteCalls++
	if _, ok := f.campaigns[[2]uuid.UUID{ws, id}]; !ok {
		return errNotFound
	}
	delete(f.campaigns, [2]uuid.UUID{ws, id})
	return nil
}

// Every remaining Store method is unused by the lifecycle tests; each is a
// zero-value stub so fakeStore satisfies the full campaign.Store interface.
func (f *fakeStore) Create(context.Context, uuid.UUID, campaign.CreateInput) (gen.Campaign, error) {
	return gen.Campaign{}, nil
}
func (f *fakeStore) List(context.Context, uuid.UUID) ([]gen.Campaign, error) { return nil, nil }
func (f *fakeStore) Stats(context.Context, uuid.UUID, uuid.UUID) (map[string]int64, error) {
	return nil, nil
}
func (f *fakeStore) CountSteps(context.Context, uuid.UUID, uuid.UUID) (int64, error) { return 0, nil }
func (f *fakeStore) EnrollTx(context.Context, uuid.UUID, uuid.UUID) ([]campaign.Enrollment, error) {
	return nil, nil
}
func (f *fakeStore) Reschedule(context.Context, uuid.UUID, uuid.UUID, time.Time) error { return nil }
func (f *fakeStore) RescheduleBatch(context.Context, uuid.UUID, map[uuid.UUID]time.Time) error {
	return nil
}
func (f *fakeStore) ListWindows(context.Context, uuid.UUID, uuid.UUID) ([]campaign.SendWindow, error) {
	return nil, nil
}
func (f *fakeStore) ReplaceSchedule(context.Context, uuid.UUID, uuid.UUID, campaign.Plan) error {
	return nil
}
func (f *fakeStore) ListSenders(context.Context, uuid.UUID, uuid.UUID) ([]campaign.Sender, error) {
	return nil, nil
}
func (f *fakeStore) FallbackSender(context.Context, uuid.UUID, uuid.UUID) (campaign.Sender, error) {
	return campaign.Sender{}, nil
}
func (f *fakeStore) ReplaceSenders(context.Context, uuid.UUID, uuid.UUID, string, []campaign.SenderInput) error {
	return nil
}
func (f *fakeStore) ListSteps(context.Context, uuid.UUID, uuid.UUID) ([]gen.SequenceStep, error) {
	return nil, nil
}
func (f *fakeStore) EnrollmentCounts(context.Context, uuid.UUID, uuid.UUID) (map[string]int64, error) {
	return nil, nil
}
func (f *fakeStore) EngagementCounts(context.Context, uuid.UUID, uuid.UUID) (int64, int64, error) {
	return 0, 0, nil
}
func (f *fakeStore) StopReasonCounts(context.Context, uuid.UUID, uuid.UUID) (map[string]int64, error) {
	return nil, nil
}
func (f *fakeStore) SetTracking(context.Context, uuid.UUID, uuid.UUID, bool) error { return nil }
func (f *fakeStore) ListEnrollments(context.Context, uuid.UUID, uuid.UUID, int32, int32) ([]gen.ListCampaignEnrollmentsRow, error) {
	return nil, nil
}

// noopChecker satisfies campaign.Checker; the lifecycle service methods never
// consult it, but NewService requires one.
type noopChecker struct{}

func (noopChecker) MailboxActive(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return true, nil
}
func (noopChecker) ListExists(context.Context, uuid.UUID, uuid.UUID) (bool, error) { return true, nil }

func newLifecycleService(ws, id uuid.UUID, status campaign.CampaignStatus) (*campaign.Service, *fakeStore) {
	store := &fakeStore{campaigns: map[[2]uuid.UUID]gen.Campaign{
		{ws, id}: {ID: id, WorkspaceID: ws, Status: string(status)},
	}}
	return campaign.NewService(store, noopChecker{}), store
}

func TestPauseOnlyFromRunning(t *testing.T) {
	cases := []struct {
		name    string
		from    campaign.CampaignStatus
		wantErr error
	}{
		{"running to paused", campaign.StatusRunning, nil},
		{"draft rejected", campaign.StatusDraft, campaign.ErrNotPausable},
		{"paused rejected", campaign.StatusPaused, campaign.ErrNotPausable},
		{"done rejected", campaign.StatusDone, campaign.ErrNotPausable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ws, id := uuid.New(), uuid.New()
			svc, store := newLifecycleService(ws, id, tc.from)

			err := svc.Pause(context.Background(), ws, id)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("Pause from %s: got %v, want %v", tc.from, err, tc.wantErr)
				}
				if store.statusCalls != 0 {
					t.Fatalf("expected store.SetStatus not called on rejected pause, got %d calls", store.statusCalls)
				}
				return
			}
			if err != nil {
				t.Fatalf("Pause: %v", err)
			}
			if got := store.campaigns[[2]uuid.UUID{ws, id}].Status; got != string(campaign.StatusPaused) {
				t.Fatalf("persisted status = %q, want %q", got, campaign.StatusPaused)
			}
		})
	}
}

func TestResumeOnlyFromPaused(t *testing.T) {
	cases := []struct {
		name    string
		from    campaign.CampaignStatus
		wantErr error
	}{
		{"paused to running", campaign.StatusPaused, nil},
		{"running rejected", campaign.StatusRunning, campaign.ErrNotResumable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ws, id := uuid.New(), uuid.New()
			svc, store := newLifecycleService(ws, id, tc.from)

			err := svc.Resume(context.Background(), ws, id)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("Resume from %s: got %v, want %v", tc.from, err, tc.wantErr)
				}
				if store.statusCalls != 0 {
					t.Fatalf("expected store.SetStatus not called on rejected resume, got %d calls", store.statusCalls)
				}
				return
			}
			if err != nil {
				t.Fatalf("Resume: %v", err)
			}
			if got := store.campaigns[[2]uuid.UUID{ws, id}].Status; got != string(campaign.StatusRunning) {
				t.Fatalf("persisted status = %q, want %q", got, campaign.StatusRunning)
			}
		})
	}
}

// TestResumeWorksForBreakerPausedCampaigns proves Resume does not distinguish
// an operator-paused campaign from a breaker-paused one: both are simply
// status='paused', and both resume the same way.
func TestResumeWorksForBreakerPausedCampaigns(t *testing.T) {
	ws, id := uuid.New(), uuid.New()
	svc, store := newLifecycleService(ws, id, campaign.StatusPaused)

	if err := svc.Resume(context.Background(), ws, id); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if got := store.campaigns[[2]uuid.UUID{ws, id}].Status; got != string(campaign.StatusRunning) {
		t.Fatalf("persisted status = %q, want %q", got, campaign.StatusRunning)
	}
}

func TestRenameValidates(t *testing.T) {
	ws, id := uuid.New(), uuid.New()
	svc, store := newLifecycleService(ws, id, campaign.StatusDraft)

	if _, err := svc.Rename(context.Background(), ws, id, ""); !errors.Is(err, campaign.ErrValidation) {
		t.Fatalf("empty name: got %v, want ErrValidation", err)
	}
	if store.renameCalls != 0 {
		t.Fatalf("expected store.Rename not called for empty name, got %d calls", store.renameCalls)
	}

	tooLong := strings.Repeat("a", 201)
	if _, err := svc.Rename(context.Background(), ws, id, tooLong); !errors.Is(err, campaign.ErrValidation) {
		t.Fatalf("201-char name: got %v, want ErrValidation", err)
	}
	if store.renameCalls != 0 {
		t.Fatalf("expected store.Rename not called for over-long name, got %d calls", store.renameCalls)
	}

	c, err := svc.Rename(context.Background(), ws, id, "New name")
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if c.Name != "New name" {
		t.Fatalf("renamed campaign name = %q, want %q", c.Name, "New name")
	}
	if store.renameCalls != 1 {
		t.Fatalf("expected exactly 1 store.Rename call, got %d", store.renameCalls)
	}
}

// TestRenameAllowedAnyStatus proves rename is not gated on lifecycle status,
// unlike Pause/Resume/DeleteDraft.
func TestRenameAllowedAnyStatus(t *testing.T) {
	for _, status := range []campaign.CampaignStatus{
		campaign.StatusDraft, campaign.StatusRunning, campaign.StatusPaused, campaign.StatusDone,
	} {
		t.Run(string(status), func(t *testing.T) {
			ws, id := uuid.New(), uuid.New()
			svc, _ := newLifecycleService(ws, id, status)
			if _, err := svc.Rename(context.Background(), ws, id, "Renamed"); err != nil {
				t.Fatalf("Rename from status %s: %v", status, err)
			}
		})
	}
}

func TestDeleteDraftOnly(t *testing.T) {
	cases := []struct {
		name    string
		status  campaign.CampaignStatus
		wantErr error
	}{
		{"draft deleted", campaign.StatusDraft, nil},
		{"running rejected", campaign.StatusRunning, campaign.ErrNotDraft},
		{"paused rejected", campaign.StatusPaused, campaign.ErrNotDraft},
		{"done rejected", campaign.StatusDone, campaign.ErrNotDraft},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ws, id := uuid.New(), uuid.New()
			svc, store := newLifecycleService(ws, id, tc.status)

			err := svc.DeleteDraft(context.Background(), ws, id)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("DeleteDraft from %s: got %v, want %v", tc.status, err, tc.wantErr)
				}
				if store.deleteCalls != 0 {
					t.Fatalf("expected store.DeleteDraft not called for non-draft, got %d calls", store.deleteCalls)
				}
				return
			}
			if err != nil {
				t.Fatalf("DeleteDraft: %v", err)
			}
			if store.deleteCalls != 1 {
				t.Fatalf("expected exactly 1 store.DeleteDraft call, got %d", store.deleteCalls)
			}
			if _, ok := store.campaigns[[2]uuid.UUID{ws, id}]; ok {
				t.Fatal("expected campaign removed from the store")
			}
		})
	}
}

// TestLifecycleWorkspaceScoped proves every lifecycle method 404s (never
// mutates) for a campaign id that belongs to a different workspace.
func TestLifecycleWorkspaceScoped(t *testing.T) {
	otherWS, callerWS, id := uuid.New(), uuid.New(), uuid.New()
	newForeignStore := func(status campaign.CampaignStatus) *fakeStore {
		return &fakeStore{campaigns: map[[2]uuid.UUID]gen.Campaign{
			{otherWS, id}: {ID: id, WorkspaceID: otherWS, Status: string(status)},
		}}
	}

	t.Run("pause", func(t *testing.T) {
		store := newForeignStore(campaign.StatusRunning)
		svc := campaign.NewService(store, noopChecker{})
		if err := svc.Pause(context.Background(), callerWS, id); !errors.Is(err, campaign.ErrNotFound) {
			t.Fatalf("Pause cross-tenant: got %v, want ErrNotFound", err)
		}
		if store.statusCalls != 0 {
			t.Fatalf("expected no mutation, got %d SetStatus calls", store.statusCalls)
		}
	})
	t.Run("resume", func(t *testing.T) {
		store := newForeignStore(campaign.StatusPaused)
		svc := campaign.NewService(store, noopChecker{})
		if err := svc.Resume(context.Background(), callerWS, id); !errors.Is(err, campaign.ErrNotFound) {
			t.Fatalf("Resume cross-tenant: got %v, want ErrNotFound", err)
		}
		if store.statusCalls != 0 {
			t.Fatalf("expected no mutation, got %d SetStatus calls", store.statusCalls)
		}
	})
	t.Run("rename", func(t *testing.T) {
		store := newForeignStore(campaign.StatusDraft)
		svc := campaign.NewService(store, noopChecker{})
		if _, err := svc.Rename(context.Background(), callerWS, id, "New name"); !errors.Is(err, campaign.ErrNotFound) {
			t.Fatalf("Rename cross-tenant: got %v, want ErrNotFound", err)
		}
		if store.renameCalls != 0 {
			t.Fatalf("expected no mutation, got %d Rename calls", store.renameCalls)
		}
	})
	t.Run("delete", func(t *testing.T) {
		store := newForeignStore(campaign.StatusDraft)
		svc := campaign.NewService(store, noopChecker{})
		if err := svc.DeleteDraft(context.Background(), callerWS, id); !errors.Is(err, campaign.ErrNotFound) {
			t.Fatalf("DeleteDraft cross-tenant: got %v, want ErrNotFound", err)
		}
		if store.deleteCalls != 0 {
			t.Fatalf("expected no mutation, got %d DeleteDraft calls", store.deleteCalls)
		}
	})
}
