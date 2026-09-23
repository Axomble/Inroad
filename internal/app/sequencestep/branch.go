package sequencestep

import (
	"context"
	"fmt"
	"slices"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/seqgraph"
)

// Graph is a campaign's steps (in step_order) and its routers — everything a
// client needs to draw the sequence as a graph.
type Graph struct {
	Steps    []gen.SequenceStep
	Branches []gen.SequenceStepBranch
}

// Graph returns the campaign's routing graph. Reading is allowed on any status.
func (s *Service) Graph(ctx context.Context, ws, campaignID uuid.UUID) (Graph, error) {
	if _, err := s.checker.CampaignStatus(ctx, ws, campaignID); err != nil {
		return Graph{}, ErrCampaignNotFound
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
// Validation happens twice, on purpose. The shape and label checks here give a
// precise error before any write; the graph check then runs AGAIN inside the
// store's transaction, under the campaign's graph lock, against the graph that
// is actually being committed — the only place a loop formed by two concurrent
// edits can be caught.
func (s *Service) SetBranch(ctx context.Context, ws, campaignID uuid.UUID, in BranchInput) (gen.SequenceStepBranch, error) {
	if _, err := s.checker.CampaignStatus(ctx, ws, campaignID); err != nil {
		return gen.SequenceStepBranch{}, ErrCampaignNotFound
	}
	if err := s.assertStepInCampaign(ctx, ws, campaignID, in.StepID); err != nil {
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
	if err := branchModel(in).ValidateShape(); err != nil {
		return gen.SequenceStepBranch{}, err
	}
	if in.ReplyLabelKey != nil {
		ok, err := s.branches.ReplyLabelExists(ctx, ws, *in.ReplyLabelKey)
		if err != nil {
			return gen.SequenceStepBranch{}, fmt.Errorf("check reply label: %w", err)
		}
		if !ok {
			return gen.SequenceStepBranch{}, &seqgraph.ShapeError{
				Code: seqgraph.CodeLabelNotAllowed,
				Msg:  fmt.Sprintf("reply label %q does not exist in this workspace", *in.ReplyLabelKey),
			}
		}
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

// DeleteBranch removes the router on one step, returning it to linear
// fall-through. Idempotent. Allowed live, for the reason SetBranch is; refused
// with a CycleError when the restored fall-through would close a loop.
func (s *Service) DeleteBranch(ctx context.Context, ws, campaignID, stepID uuid.UUID) error {
	if _, err := s.checker.CampaignStatus(ctx, ws, campaignID); err != nil {
		return ErrCampaignNotFound
	}
	if err := s.assertStepInCampaign(ctx, ws, campaignID, stepID); err != nil {
		return err
	}
	return s.branches.DeleteBranch(ctx, ws, campaignID, stepID, checkGraph)
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
