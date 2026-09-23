package sequencestep

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/seqgraph"
)

// Graph is a campaign's steps (in step_order) and its routers — everything a
// client needs to draw the sequence as a graph.
type Graph struct {
	Steps    []gen.SequenceStep
	Branches []gen.SequenceStepBranch
}

// requireCampaign resolves the campaign in the workspace. Only a genuine miss
// is "campaign not found" (404); any other failure is a server error and is
// returned as one, so a database outage is not reported to the client as a
// campaign that does not exist.
func (s *Service) requireCampaign(ctx context.Context, ws, campaignID uuid.UUID) error {
	if _, err := s.checker.CampaignStatus(ctx, ws, campaignID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrCampaignNotFound
		}
		return fmt.Errorf("campaign status: %w", err)
	}
	return nil
}

// Graph returns the campaign's routing graph. Reading is allowed on any status.
func (s *Service) Graph(ctx context.Context, ws, campaignID uuid.UUID) (Graph, error) {
	if err := s.requireCampaign(ctx, ws, campaignID); err != nil {
		return Graph{}, err
	}
	steps, err := s.store.List(ctx, ws, campaignID)
	if err != nil {
		return Graph{}, fmt.Errorf("list steps: %w", err)
	}
	branches, err := s.branches.ListBranches(ctx, ws, campaignID)
	if err != nil {
		return Graph{}, fmt.Errorf("list branches: %w", err)
	}
	return Graph{Steps: steps, Branches: branches}, nil
}

// SetBranch creates or replaces the router on one step.
//
// Allowed on a RUNNING campaign, like a content or variant edit and unlike a
// step create/delete/reorder. The send path re-reads the graph on every advance
// (see the mid-flight rule on inprocess.routeGraph), so an edit governs every
// decision made after it commits and none made before — which is the same
// live-reference contract a body edit has.
//
// Validation happens twice, on purpose. The shape, label and evidence checks
// here give a precise error before any write; the graph check then runs AGAIN
// inside the store's transaction, under the campaign's graph lock, against the
// graph that is actually being committed — the only place a loop formed by two
// concurrent edits can be caught.
//
// in.Expect, when set, is enforced by that same write (see
// BranchStore.UpsertBranch). It is checked after validation, so a request that
// is both stale and malformed is told it is malformed.
func (s *Service) SetBranch(ctx context.Context, ws, campaignID uuid.UUID, in BranchInput) (gen.SequenceStepBranch, error) {
	if err := s.requireCampaign(ctx, ws, campaignID); err != nil {
		return gen.SequenceStepBranch{}, err
	}
	step, err := s.stepInCampaign(ctx, ws, campaignID, in.StepID)
	if err != nil {
		return gen.SequenceStepBranch{}, err
	}
	// The model reads an absent window as 0, which is exactly right for a real
	// condition (0 is out of range) but would let an explicit "within_days": 0 on
	// an 'always' branch through; refuse any window on 'always' here.
	if in.Condition == string(seqgraph.Always) && in.WithinDays != nil {
		return gen.SequenceStepBranch{}, &seqgraph.ShapeError{
			Code: seqgraph.CodeInvalidWithinDays, Msg: "within_days is not allowed on an 'always' branch",
		}
	}
	model := branchModel(in)
	if err := model.ValidateShape(); err != nil {
		return gen.SequenceStepBranch{}, err
	}
	if err := s.checkReplyLabel(ctx, ws, in.ReplyLabelKey); err != nil {
		return gen.SequenceStepBranch{}, err
	}
	if err := s.checkTrackable(ctx, ws, campaignID, step, model.Condition.Signal()); err != nil {
		return gen.SequenceStepBranch{}, err
	}
	// Refuse an exit to a step outside the campaign BEFORE writing: the
	// composite FK would refuse it too, but as a constraint violation rather than
	// an error that names the offending target.
	steps, err := s.store.List(ctx, ws, campaignID)
	if err != nil {
		return gen.SequenceStepBranch{}, fmt.Errorf("list steps: %w", err)
	}
	for _, target := range []*uuid.UUID{in.YesStepID, in.NoStepID} {
		if target != nil && !containsStep(steps, *target) {
			return gen.SequenceStepBranch{}, &seqgraph.TargetError{StepID: in.StepID, Target: *target}
		}
	}
	in.CampaignID = campaignID
	return s.branches.UpsertBranch(ctx, ws, in, checkGraph)
}

// checkReplyLabel refuses a label that could never fire a branch.
//
// Reply labels decide what a reply DOES to an enrollment, and a branch never
// overrides that (docs/security.md invariant 84). A label that stops the
// enrollment — the default for every builtin human label — ends the sequence
// the moment such a reply arrives, before any branch is consulted, so a branch
// naming it would silently never route anyone. Refusing it at save time is the
// honest answer; the operator can clear stops_enrollment on the label (or pick
// one that does not stop) if routing on it is what they want.
//
// Checked at save time only. A label edited to stop AFTER a branch names it
// does not break anything: the reply stops the sequence, which is what the label
// now says, and the branch simply never fires.
func (s *Service) checkReplyLabel(ctx context.Context, ws uuid.UUID, key *string) error {
	if key == nil {
		return nil
	}
	stops, found, err := s.branches.ReplyLabelStops(ctx, ws, *key)
	if err != nil {
		return fmt.Errorf("check reply label: %w", err)
	}
	if !found {
		return &seqgraph.ShapeError{
			Code: seqgraph.CodeLabelNotAllowed,
			Msg:  fmt.Sprintf("reply label %q does not exist in this workspace", *key),
		}
	}
	if stops {
		return &seqgraph.ShapeError{
			Code: seqgraph.CodeLabelStopsSequence,
			Msg: fmt.Sprintf("reply label %q stops the sequence, so a reply with it can never be routed; "+
				"use a label that does not stop the enrollment", *key),
		}
	}
	return nil
}

// checkTrackable refuses an open/click condition that has no evidence to read.
//
// Opens and clicks exist only when the campaign has tracking on AND the step
// (every copy of it — the base and each A/B variant) has an HTML body for the
// pixel and the rewritten links to live in. Without both, not one open or click
// will ever be recorded: "opened" would route every contact NO at the deadline
// and "not_opened" every contact YES, which looks like a working branch and is
// really a fixed route with a delay.
//
// Refused at save time rather than degraded, because the refusal is the only
// point where the operator can see why. Turning tracking off or editing a step
// to text-only AFTER the branch exists is not blocked — those edits belong to
// other endpoints (and, for tracking, another domain), and blocking a tracking
// toggle on account of a branch would make a privacy setting hostage to
// sequence design. Such a branch then takes the no-evidence route at its
// deadline, which is the documented behaviour.
func (s *Service) checkTrackable(ctx context.Context, ws, campaignID uuid.UUID, step gen.SequenceStep, signal seqgraph.Signal) error {
	if signal != seqgraph.SignalOpen && signal != seqgraph.SignalClick {
		return nil
	}
	enabled, err := s.branches.TrackingEnabled(ctx, ws, campaignID)
	if err != nil {
		return fmt.Errorf("check tracking: %w", err)
	}
	if !enabled {
		return &seqgraph.ShapeError{
			Code: seqgraph.CodeTrackingRequired,
			Msg:  "open and click conditions need tracking turned on for this campaign",
		}
	}
	variants, err := s.variants.ListForStep(ctx, ws, step.ID)
	if err != nil {
		return fmt.Errorf("list step variants: %w", err)
	}
	textOnly := step.BodyHtml == "" ||
		slices.ContainsFunc(variants, func(v Variant) bool { return v.BodyHTML == "" })
	if textOnly {
		return &seqgraph.ShapeError{
			Code: seqgraph.CodeTrackingRequired,
			Msg:  "open and click conditions need an HTML body on this step and every one of its variants",
		}
	}
	return nil
}

// DeleteBranch removes the router on one step, returning it to linear
// fall-through. Idempotent without a precondition. Allowed live, for the reason
// SetBranch is; refused with a CycleError when the restored fall-through would
// close a loop, and with a *BranchChangedError when expect no longer holds.
func (s *Service) DeleteBranch(ctx context.Context, ws, campaignID, stepID uuid.UUID, expect BranchPrecondition) error {
	if err := s.requireCampaign(ctx, ws, campaignID); err != nil {
		return err
	}
	if _, err := s.stepInCampaign(ctx, ws, campaignID, stepID); err != nil {
		return err
	}
	return s.branches.DeleteBranch(ctx, ws, campaignID, stepID, expect, checkGraph)
}

// checkGraph is the one GraphCheck every graph-changing write commits under.
func checkGraph(steps []gen.SequenceStep, branches []gen.SequenceStepBranch) error {
	return BuildGraph(steps, branches).Validate()
}

// BuildGraph converts the persistence rows into the routing model. Exported so
// the handler can compute each step's fall-through with the SAME rule the send
// path and the validator use, rather than re-deriving "next by step_order".
func BuildGraph(steps []gen.SequenceStep, branches []gen.SequenceStepBranch) seqgraph.Graph {
	nodes := make([]seqgraph.Step, len(steps))
	for i, st := range steps {
		nodes[i] = seqgraph.Step{ID: st.ID, Order: st.StepOrder, DelaySeconds: st.DelaySeconds}
	}
	routers := make([]seqgraph.Branch, len(branches))
	for i, b := range branches {
		routers[i] = BranchFromRow(b)
	}
	return seqgraph.New(nodes, routers)
}

// BranchFromRow converts one stored router into the routing model.
func BranchFromRow(b gen.SequenceStepBranch) seqgraph.Branch {
	out := seqgraph.Branch{StepID: b.StepID, Condition: seqgraph.Condition(b.Condition)}
	if b.WithinDays != nil {
		out.WithinDays = int(*b.WithinDays)
	}
	if b.ReplyLabelKey != nil {
		out.ReplyLabelKey = *b.ReplyLabelKey
	}
	if b.YesStepID.Valid {
		out.Yes = b.YesStepID.Bytes
	}
	if b.NoStepID.Valid {
		out.No = b.NoStepID.Bytes
	}
	return out
}

// branchModel converts a write request into the routing model for validation.
func branchModel(in BranchInput) seqgraph.Branch {
	out := seqgraph.Branch{StepID: in.StepID, Condition: seqgraph.Condition(in.Condition)}
	if in.WithinDays != nil {
		out.WithinDays = int(*in.WithinDays)
	}
	if in.ReplyLabelKey != nil {
		out.ReplyLabelKey = *in.ReplyLabelKey
	}
	if in.YesStepID != nil {
		out.Yes = *in.YesStepID
	}
	if in.NoStepID != nil {
		out.No = *in.NoStepID
	}
	return out
}

func containsStep(steps []gen.SequenceStep, id uuid.UUID) bool {
	return slices.ContainsFunc(steps, func(st gen.SequenceStep) bool { return st.ID == id })
}
