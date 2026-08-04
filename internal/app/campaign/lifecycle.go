package campaign

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/validate"
)

// Sentinel errors for the operator lifecycle actions (pause/resume/rename/
// delete-draft). The handler maps every one of these to 409, with a JSON
// {"error": "..."} body (httpx.Error).
var (
	ErrNotPausable  = errors.New("campaign is not running and cannot be paused")
	ErrNotResumable = errors.New("campaign is not paused and cannot be resumed")
	ErrNotDraft     = errors.New("campaign is not a draft")
)

// Pause stops a running campaign: sends already queued or in flight are
// unaffected, but no further step advances are dispatched (the worker checks
// status before every send). Allowed only from running; anything else
// (draft/paused/done) is ErrNotPausable.
func (s *Service) Pause(ctx context.Context, ws, id uuid.UUID) error {
	c, err := s.store.Get(ctx, ws, id)
	if err != nil {
		return ErrNotFound
	}
	if c.Status != string(StatusRunning) {
		return ErrNotPausable
	}
	return s.store.SetStatus(ctx, ws, id, StatusPaused)
}

// Resume restarts a paused campaign. Allowed only from paused; anything else
// is ErrNotResumable. Resume treats an operator-paused and a breaker-paused
// campaign identically -- both are simply status='paused', and campaigns.status
// carries no "who paused it" distinction (see campaign_pause_events for the
// breaker's own append-only history, which this does not touch).
//
// This does NOT enqueue anything to nudge the pipeline. Launch's Enqueuer
// seam (see service.go) only knows how to schedule ONE enrollment's next
// advance (EnqueueAdvanceAt) -- there is no clean way to target "every
// enrollment on this campaign" through it, and the sequence-sweeper task
// (queue.TaskSweepEnrollments) has no ad hoc single-shot enqueue of its own;
// it only runs on its periodic 5-minute schedule (queue.RegisterSweepEnrollments).
// Bolting a campaign-scoped enqueue loop or a new platform/queue method onto
// this task was out of scope, per the brief's stated fallback: resumed
// enrollments are picked up by that periodic sweep like any other stuck-due
// enrollment -- at most a 5-minute delay, not a correctness gap.
func (s *Service) Resume(ctx context.Context, ws, id uuid.UUID) error {
	c, err := s.store.Get(ctx, ws, id)
	if err != nil {
		return ErrNotFound
	}
	if c.Status != string(StatusPaused) {
		return ErrNotResumable
	}
	return s.store.SetStatus(ctx, ws, id, StatusRunning)
}

// renameInput carries just the field Rename validates, so it can reuse
// validate.Struct with the exact same tag Create's name field uses.
type renameInput struct {
	Name string `validate:"required,min=1,max=200"`
}

// Rename replaces the campaign's display name. Allowed at any lifecycle
// status -- unlike Pause/Resume/DeleteDraft, a name is cosmetic and never
// affects sending. The name is validated (min=1,max=200, mirroring Create's
// CreateInput.Name) before the workspace-scoped lookup runs.
func (s *Service) Rename(ctx context.Context, ws, id uuid.UUID, name string) (gen.Campaign, error) {
	if err := validate.Struct(renameInput{Name: name}); err != nil {
		return gen.Campaign{}, ErrValidation
	}
	if _, err := s.store.Get(ctx, ws, id); err != nil {
		return gen.Campaign{}, ErrNotFound
	}
	return s.store.Rename(ctx, ws, id, name)
}

// DeleteDraft removes a campaign that has never been launched. Allowed only
// from draft; anything else is ErrNotDraft -- a launched campaign's sends,
// enrollments and history must never be silently discarded. The store deletes
// the draft's dependents (send windows, campaign_senders, sequence_steps,
// sequence_enrollments) and the campaign row itself in one transaction.
func (s *Service) DeleteDraft(ctx context.Context, ws, id uuid.UUID) error {
	c, err := s.store.Get(ctx, ws, id)
	if err != nil {
		return ErrNotFound
	}
	if c.Status != string(StatusDraft) {
		return ErrNotDraft
	}
	return s.store.DeleteDraft(ctx, ws, id)
}
