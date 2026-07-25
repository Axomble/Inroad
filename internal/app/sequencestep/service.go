package sequencestep

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// Sentinel errors the handler maps to HTTP status codes.
var (
	ErrNotFound         = errors.New("step not found")
	ErrCampaignNotFound = errors.New("campaign not found")
	// ErrCampaignNotDraft guards structural edits (create/delete/reorder): the
	// spec permits them only while the campaign is draft; a running campaign
	// returns 409. Content edits (Update) are exempt — they are live-reference.
	ErrCampaignNotDraft = errors.New("campaign is not draft")
	// ErrInvalidOrder is returned when a reorder request's step_ids is not
	// exactly a permutation of the campaign's current step ids (wrong length,
	// duplicates, extras, or a foreign/other-campaign id). Handler maps it to 400.
	ErrInvalidOrder = errors.New("step_ids must be a permutation of the campaign's step ids")
)

// draftStatus is the campaign status that permits structural step edits. Kept
// local so this domain doesn't import the campaign package (app/* isolation).
const draftStatus = "draft"

// Service implements the step use cases. It depends on the Store and
// CampaignChecker interfaces, not concrete stores (dependency inversion).
type Service struct {
	store   Store
	checker CampaignChecker
}

func NewService(store Store, checker CampaignChecker) *Service {
	return &Service{store: store, checker: checker}
}

// Create appends a step at max(step_order)+1. Structural change → requires the
// campaign to exist in the workspace and be draft.
func (s *Service) Create(ctx context.Context, ws, campaignID uuid.UUID, in CreateInput) (gen.SequenceStep, error) {
	if err := s.requireDraft(ctx, ws, campaignID); err != nil {
		return gen.SequenceStep{}, err
	}
	maxOrder, err := s.store.MaxStepOrder(ctx, ws, campaignID)
	if err != nil {
		return gen.SequenceStep{}, err
	}
	in.CampaignID = campaignID
	in.StepOrder = maxOrder + 1
	return s.store.Create(ctx, ws, in)
}

// List returns the campaign's steps in order.
func (s *Service) List(ctx context.Context, ws, campaignID uuid.UUID) ([]gen.SequenceStep, error) {
	return s.store.List(ctx, ws, campaignID)
}

// Update edits a step's content. Live-reference: allowed on any campaign
// status (edited body applies to future sends). Verifies the step belongs to
// the campaign named in the request so a step id can't be edited via another
// campaign's URL.
func (s *Service) Update(ctx context.Context, ws, campaignID uuid.UUID, in UpdateInput) (gen.SequenceStep, error) {
	if _, err := s.checker.CampaignStatus(ctx, ws, campaignID); err != nil {
		return gen.SequenceStep{}, ErrCampaignNotFound
	}
	if err := s.assertStepInCampaign(ctx, ws, campaignID, in.StepID); err != nil {
		return gen.SequenceStep{}, err
	}
	st, err := s.store.Update(ctx, ws, in)
	if err != nil {
		return gen.SequenceStep{}, ErrNotFound
	}
	return st, nil
}

// Delete removes a step. Structural change → requires the campaign to be
// draft (running/paused/done return 409).
func (s *Service) Delete(ctx context.Context, ws, campaignID, stepID uuid.UUID) error {
	if err := s.requireDraft(ctx, ws, campaignID); err != nil {
		return err
	}
	if err := s.assertStepInCampaign(ctx, ws, campaignID, stepID); err != nil {
		return err
	}
	return s.store.Delete(ctx, ws, stepID)
}

// Reorder rewrites the campaign's step_order to match stepIDs' order.
// Structural change → draft-only (409 otherwise). stepIDs must be exactly a
// permutation of the campaign's current step ids — any mismatch (including a
// foreign/other-tenant id, which the workspace-pinned List simply won't
// contain) yields ErrInvalidOrder (400) before any write. Returns the steps in
// their new order.
func (s *Service) Reorder(ctx context.Context, ws, campaignID uuid.UUID, stepIDs []uuid.UUID) ([]gen.SequenceStep, error) {
	if err := s.requireDraft(ctx, ws, campaignID); err != nil {
		return nil, err
	}
	current, err := s.store.List(ctx, ws, campaignID)
	if err != nil {
		return nil, err
	}
	if err := validatePermutation(current, stepIDs); err != nil {
		return nil, err
	}
	return s.store.Reorder(ctx, ws, campaignID, stepIDs)
}

// validatePermutation confirms stepIDs contains each of current's ids exactly
// once (same set, no dupes, no extras, no missing). current comes from the
// workspace-pinned List, so a cross-tenant/foreign id is absent from the set
// and rejected here.
func validatePermutation(current []gen.SequenceStep, stepIDs []uuid.UUID) error {
	if len(stepIDs) != len(current) {
		return ErrInvalidOrder
	}
	want := make(map[uuid.UUID]struct{}, len(current))
	for _, st := range current {
		want[st.ID] = struct{}{}
	}
	seen := make(map[uuid.UUID]struct{}, len(stepIDs))
	for _, id := range stepIDs {
		if _, ok := want[id]; !ok {
			return ErrInvalidOrder
		}
		if _, dup := seen[id]; dup {
			return ErrInvalidOrder
		}
		seen[id] = struct{}{}
	}
	return nil
}

// requireDraft returns ErrCampaignNotFound if the campaign isn't in the
// workspace, ErrCampaignNotDraft if it isn't draft.
func (s *Service) requireDraft(ctx context.Context, ws, campaignID uuid.UUID) error {
	status, err := s.checker.CampaignStatus(ctx, ws, campaignID)
	if err != nil {
		return ErrCampaignNotFound
	}
	if status != draftStatus {
		return ErrCampaignNotDraft
	}
	return nil
}

// assertStepInCampaign confirms the step exists in the workspace AND belongs to
// campaignID; otherwise ErrNotFound (never leaks another campaign's/tenant's
// step).
func (s *Service) assertStepInCampaign(ctx context.Context, ws, campaignID, stepID uuid.UUID) error {
	st, err := s.store.Get(ctx, ws, stepID)
	if err != nil || st.CampaignID != campaignID {
		return ErrNotFound
	}
	return nil
}
