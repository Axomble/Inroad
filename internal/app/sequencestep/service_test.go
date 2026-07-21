package sequencestep

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// fakeStore records the last create/update and can simulate a not-found Get
// (for the cross-tenant / wrong-campaign guards).
type fakeStore struct {
	maxOrder  int32
	created   CreateInput
	updated   UpdateInput
	deletedID uuid.UUID
	getStep   gen.SequenceStep
	getErr    error
}

func (f *fakeStore) Create(_ context.Context, ws uuid.UUID, in CreateInput) (gen.SequenceStep, error) {
	f.created = in
	return gen.SequenceStep{ID: uuid.New(), CampaignID: in.CampaignID, StepOrder: in.StepOrder, Subject: in.Subject}, nil
}
func (f *fakeStore) Get(context.Context, uuid.UUID, uuid.UUID) (gen.SequenceStep, error) {
	return f.getStep, f.getErr
}
func (f *fakeStore) List(context.Context, uuid.UUID, uuid.UUID) ([]gen.SequenceStep, error) {
	return nil, nil
}
func (f *fakeStore) Update(_ context.Context, _ uuid.UUID, in UpdateInput) (gen.SequenceStep, error) {
	f.updated = in
	return gen.SequenceStep{ID: in.StepID, Subject: in.Subject}, nil
}
func (f *fakeStore) Delete(_ context.Context, _, id uuid.UUID) error { f.deletedID = id; return nil }
func (f *fakeStore) MaxStepOrder(context.Context, uuid.UUID, uuid.UUID) (int32, error) {
	return f.maxOrder, nil
}

type fakeChecker struct {
	status string
	err    error
}

func (c fakeChecker) CampaignStatus(context.Context, uuid.UUID, uuid.UUID) (string, error) {
	return c.status, c.err
}

func TestCreateRejectsNonDraftCampaign(t *testing.T) {
	svc := NewService(&fakeStore{}, fakeChecker{status: "running"})
	_, err := svc.Create(context.Background(), uuid.New(), uuid.New(), CreateInput{Subject: "x", BodyText: "y"})
	if !errors.Is(err, ErrCampaignNotDraft) {
		t.Fatalf("want ErrCampaignNotDraft, got %v", err)
	}
}

func TestCreateRejectsMissingCampaign(t *testing.T) {
	svc := NewService(&fakeStore{}, fakeChecker{err: errors.New("no rows")})
	_, err := svc.Create(context.Background(), uuid.New(), uuid.New(), CreateInput{Subject: "x", BodyText: "y"})
	if !errors.Is(err, ErrCampaignNotFound) {
		t.Fatalf("want ErrCampaignNotFound, got %v", err)
	}
}

func TestCreateAppendsAtNextOrder(t *testing.T) {
	store := &fakeStore{maxOrder: 2}
	svc := NewService(store, fakeChecker{status: "draft"})
	campaignID := uuid.New()
	st, err := svc.Create(context.Background(), uuid.New(), campaignID, CreateInput{Subject: "x", BodyText: "y"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if st.StepOrder != 3 {
		t.Fatalf("want step_order 3 (max+1), got %d", st.StepOrder)
	}
	if store.created.CampaignID != campaignID {
		t.Fatalf("campaign id not threaded into create input")
	}
}

// Content edits are live-reference: allowed even when the campaign is running.
func TestUpdateAllowedOnRunningCampaign(t *testing.T) {
	campaignID := uuid.New()
	stepID := uuid.New()
	store := &fakeStore{getStep: gen.SequenceStep{ID: stepID, CampaignID: campaignID}}
	svc := NewService(store, fakeChecker{status: "running"})
	_, err := svc.Update(context.Background(), uuid.New(), campaignID, UpdateInput{StepID: stepID, Subject: "new"})
	if err != nil {
		t.Fatalf("update on running should be allowed (live-reference), got %v", err)
	}
	if store.updated.Subject != "new" {
		t.Fatalf("update not applied")
	}
}

// Delete is structural: forbidden on a running campaign (409).
func TestDeleteRejectsRunningCampaign(t *testing.T) {
	campaignID := uuid.New()
	stepID := uuid.New()
	store := &fakeStore{getStep: gen.SequenceStep{ID: stepID, CampaignID: campaignID}}
	svc := NewService(store, fakeChecker{status: "running"})
	err := svc.Delete(context.Background(), uuid.New(), campaignID, stepID)
	if !errors.Is(err, ErrCampaignNotDraft) {
		t.Fatalf("want ErrCampaignNotDraft, got %v", err)
	}
}

// A step id belonging to a different campaign must not be editable via another
// campaign's URL, even within the same workspace.
func TestUpdateRejectsStepFromAnotherCampaign(t *testing.T) {
	urlCampaign := uuid.New()
	otherCampaign := uuid.New()
	stepID := uuid.New()
	store := &fakeStore{getStep: gen.SequenceStep{ID: stepID, CampaignID: otherCampaign}}
	svc := NewService(store, fakeChecker{status: "draft"})
	_, err := svc.Update(context.Background(), uuid.New(), urlCampaign, UpdateInput{StepID: stepID, Subject: "x"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound for mismatched campaign, got %v", err)
	}
}
