package sequencestep

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/seqgraph"
)

// fakeBranchStore keeps routers in memory and, like the real store, runs the
// GraphCheck against the graph AS IT WOULD BE COMMITTED, rolling back on error.
// steps is the campaign's step list the check sees.
type fakeBranchStore struct {
	steps    []gen.SequenceStep
	branches map[uuid.UUID]gen.SequenceStepBranch
	labels   map[string]bool
	upserts  int
}

func (f *fakeBranchStore) ListBranches(context.Context, uuid.UUID, uuid.UUID) ([]gen.SequenceStepBranch, error) {
	return f.list(), nil
}

func (f *fakeBranchStore) ReplyLabelExists(_ context.Context, _ uuid.UUID, key string) (bool, error) {
	return f.labels[key], nil
}

func (f *fakeBranchStore) UpsertBranch(_ context.Context, ws uuid.UUID, in BranchInput, check GraphCheck) (gen.SequenceStepBranch, error) {
	row := gen.SequenceStepBranch{
		StepID: in.StepID, WorkspaceID: ws, CampaignID: in.CampaignID, Condition: in.Condition,
		WithinDays: in.WithinDays, ReplyLabelKey: in.ReplyLabelKey,
		YesStepID: nullUUID(in.YesStepID), NoStepID: nullUUID(in.NoStepID),
	}
	prev, had := f.branches[in.StepID]
	if f.branches == nil {
		f.branches = map[uuid.UUID]gen.SequenceStepBranch{}
	}
	f.branches[in.StepID] = row
	if err := check(f.steps, f.list()); err != nil {
		if had {
			f.branches[in.StepID] = prev
		} else {
			delete(f.branches, in.StepID)
		}
		return gen.SequenceStepBranch{}, err
	}
	f.upserts++
	return row, nil
}

func (f *fakeBranchStore) DeleteBranch(_ context.Context, _, _, stepID uuid.UUID, check GraphCheck) error {
	prev, had := f.branches[stepID]
	delete(f.branches, stepID)
	if err := check(f.steps, f.list()); err != nil {
		if had {
			f.branches[stepID] = prev
		}
		return err
	}
	return nil
}

func (f *fakeBranchStore) list() []gen.SequenceStepBranch {
	out := make([]gen.SequenceStepBranch, 0, len(f.branches))
	for _, b := range f.branches {
		out = append(out, b)
	}
	return out
}

// branchFixture is a campaign with three linear steps and both fakes wired so
// the service's own step lookups and the store's graph check see the same steps.
type branchFixture struct {
	svc      *Service
	branches *fakeBranchStore
	campaign uuid.UUID
	ws       uuid.UUID
	step     []uuid.UUID
}

func newBranchFixture(t *testing.T, status string) branchFixture {
	t.Helper()
	campaign := uuid.New()
	var steps []gen.SequenceStep
	var ids []uuid.UUID
	for i := range 3 {
		id := uuid.New()
		ids = append(ids, id)
		steps = append(steps, gen.SequenceStep{ID: id, CampaignID: campaign, StepOrder: int32(i + 1)})
	}
	bs := &fakeBranchStore{steps: steps, labels: map[string]bool{"positive": true}}
	store := &stepsByID{steps: steps}
	return branchFixture{
		svc:      NewService(store, fakeChecker{status: status}, &fakeVariantStore{}, bs),
		branches: bs, campaign: campaign, ws: uuid.New(), step: ids,
	}
}

// stepsByID is a Store whose Get and List answer from a fixed step set, so
// assertStepInCampaign behaves as it does against the database.
type stepsByID struct {
	fakeStore
	steps []gen.SequenceStep
}

func (s *stepsByID) Get(_ context.Context, _, id uuid.UUID) (gen.SequenceStep, error) {
	for _, st := range s.steps {
		if st.ID == id {
			return st, nil
		}
	}
	return gen.SequenceStep{}, errors.New("no rows")
}

func (s *stepsByID) List(context.Context, uuid.UUID, uuid.UUID) ([]gen.SequenceStep, error) {
	return s.steps, nil
}

func ptr[T any](v T) *T { return &v }

func TestSetBranchHappyPathOnRunningCampaign(t *testing.T) {
	f := newBranchFixture(t, "running")
	b, err := f.svc.SetBranch(context.Background(), f.ws, f.campaign, BranchInput{
		StepID: f.step[0], Condition: "opened", WithinDays: ptr(int32(3)),
		YesStepID: &f.step[2], NoStepID: &f.step[1],
	})
	if err != nil {
		t.Fatalf("a branch edit is allowed live: %v", err)
	}
	if b.CampaignID != f.campaign || b.Condition != "opened" || !b.YesStepID.Valid || b.YesStepID.Bytes != f.step[2] {
		t.Fatalf("stored branch = %+v", b)
	}
}

func TestSetBranchRejectsMissingCampaign(t *testing.T) {
	f := newBranchFixture(t, "draft")
	svc := NewService(&fakeStore{}, fakeChecker{err: errors.New("no rows")}, &fakeVariantStore{}, f.branches)
	_, err := svc.SetBranch(context.Background(), f.ws, f.campaign, BranchInput{StepID: f.step[0], Condition: "always"})
	if !errors.Is(err, ErrCampaignNotFound) {
		t.Fatalf("want ErrCampaignNotFound, got %v", err)
	}
}

// A step of a different campaign (or tenant — the workspace-pinned Get simply
// does not find it) is not routable through this campaign's URL.
func TestSetBranchRejectsStepFromAnotherCampaign(t *testing.T) {
	f := newBranchFixture(t, "draft")
	_, err := f.svc.SetBranch(context.Background(), f.ws, f.campaign, BranchInput{StepID: uuid.New(), Condition: "always"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if f.branches.upserts != 0 {
		t.Fatal("nothing may be written for a foreign step")
	}
}

func TestSetBranchRejectsTargetOutsideCampaign(t *testing.T) {
	f := newBranchFixture(t, "draft")
	foreign := uuid.New()
	_, err := f.svc.SetBranch(context.Background(), f.ws, f.campaign, BranchInput{
		StepID: f.step[0], Condition: "replied", WithinDays: ptr(int32(2)), NoStepID: &foreign,
	})
	var te *seqgraph.TargetError
	if !errors.As(err, &te) || te.Target != foreign {
		t.Fatalf("want TargetError naming %v, got %v", foreign, err)
	}
	if f.branches.upserts != 0 {
		t.Fatal("an unknown target must be refused before any write")
	}
}

func TestSetBranchRejectsCycle(t *testing.T) {
	f := newBranchFixture(t, "running")
	// 3 -> 1 closes 1 -> 2 -> 3 (implicit fall-through).
	_, err := f.svc.SetBranch(context.Background(), f.ws, f.campaign, BranchInput{
		StepID: f.step[2], Condition: "always", YesStepID: &f.step[0],
	})
	var cyc *seqgraph.CycleError
	if !errors.As(err, &cyc) {
		t.Fatalf("want CycleError, got %v", err)
	}
	if len(f.branches.branches) != 0 {
		t.Fatal("a refused edit must leave the graph as it was")
	}
}

func TestSetBranchShapeErrors(t *testing.T) {
	cases := map[string]struct {
		in   func(f branchFixture) BranchInput
		code string
	}{
		"unknown condition": {func(f branchFixture) BranchInput {
			return BranchInput{StepID: f.step[0], Condition: "bounced", WithinDays: ptr(int32(1))}
		}, seqgraph.CodeInvalidCondition},
		"missing window": {func(f branchFixture) BranchInput {
			return BranchInput{StepID: f.step[0], Condition: "clicked"}
		}, seqgraph.CodeInvalidWithinDays},
		"explicit zero window on always": {func(f branchFixture) BranchInput {
			return BranchInput{StepID: f.step[0], Condition: "always", WithinDays: ptr(int32(0))}
		}, seqgraph.CodeInvalidWithinDays},
		"no exit on always": {func(f branchFixture) BranchInput {
			return BranchInput{StepID: f.step[0], Condition: "always", NoStepID: &f.step[1]}
		}, seqgraph.CodeNoExitNotAllowed},
		"label on an open": {func(f branchFixture) BranchInput {
			return BranchInput{StepID: f.step[0], Condition: "opened", WithinDays: ptr(int32(1)), ReplyLabelKey: ptr("positive")}
		}, seqgraph.CodeLabelNotAllowed},
		"label that does not exist": {func(f branchFixture) BranchInput {
			return BranchInput{StepID: f.step[0], Condition: "replied", WithinDays: ptr(int32(1)), ReplyLabelKey: ptr("ghost")}
		}, seqgraph.CodeLabelNotAllowed},
		"self loop": {func(f branchFixture) BranchInput {
			return BranchInput{StepID: f.step[1], Condition: "not_opened", WithinDays: ptr(int32(1)), YesStepID: &f.step[1]}
		}, seqgraph.CodeCycle},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newBranchFixture(t, "draft")
			_, err := f.svc.SetBranch(context.Background(), f.ws, f.campaign, tc.in(f))
			if got := seqgraph.CodeOf(err); got != tc.code {
				t.Fatalf("code = %q (%v), want %q", got, err, tc.code)
			}
			if f.branches.upserts != 0 {
				t.Fatal("a malformed branch must not be written")
			}
		})
	}
}

// Removing a router restores the step's fall-through, which is itself an edge:
// here step 2 routes back to step 1, which is only acyclic because step 1 ends
// explicitly. Deleting step 1's router would restore 1 -> 2 and close the loop.
func TestDeleteBranchRefusedWhenFallThroughClosesLoop(t *testing.T) {
	f := newBranchFixture(t, "running")
	ctx := context.Background()
	if _, err := f.svc.SetBranch(ctx, f.ws, f.campaign, BranchInput{StepID: f.step[0], Condition: "always"}); err != nil {
		t.Fatalf("1 ends: %v", err)
	}
	if _, err := f.svc.SetBranch(ctx, f.ws, f.campaign, BranchInput{
		StepID: f.step[1], Condition: "always", YesStepID: &f.step[0],
	}); err != nil {
		t.Fatalf("2 -> 1 while 1 ends is acyclic: %v", err)
	}
	err := f.svc.DeleteBranch(ctx, f.ws, f.campaign, f.step[0])
	if seqgraph.CodeOf(err) != seqgraph.CodeCycle {
		t.Fatalf("want a cycle refusal, got %v", err)
	}
	if _, still := f.branches.branches[f.step[0]]; !still {
		t.Fatal("the refused delete must be rolled back")
	}
}

func TestDeleteBranchIsIdempotent(t *testing.T) {
	f := newBranchFixture(t, "running")
	if err := f.svc.DeleteBranch(context.Background(), f.ws, f.campaign, f.step[0]); err != nil {
		t.Fatalf("deleting a router that does not exist is a no-op: %v", err)
	}
}

func TestGraphReturnsStepsAndBranches(t *testing.T) {
	f := newBranchFixture(t, "running")
	f.branches.branches = map[uuid.UUID]gen.SequenceStepBranch{
		f.step[0]: {StepID: f.step[0], Condition: "always", YesStepID: pgtype.UUID{Bytes: f.step[2], Valid: true}},
	}
	g, err := f.svc.Graph(context.Background(), f.ws, f.campaign)
	if err != nil {
		t.Fatalf("Graph: %v", err)
	}
	if len(g.Steps) != 3 || len(g.Branches) != 1 {
		t.Fatalf("graph = %+v", g)
	}
	resp := toGraphResponse(f.campaign, g)
	if resp.EntryStepID == nil || *resp.EntryStepID != f.step[0].String() {
		t.Fatalf("entry = %v", resp.EntryStepID)
	}
	if resp.Nodes[0].Branch == nil || resp.Nodes[0].Branch.YesStepID == nil || *resp.Nodes[0].Branch.YesStepID != f.step[2].String() {
		t.Fatalf("node 1 branch = %+v", resp.Nodes[0].Branch)
	}
	// The fall-through is served whether or not a branch overrides it.
	if resp.Nodes[0].DefaultNextStepID == nil || *resp.Nodes[0].DefaultNextStepID != f.step[1].String() {
		t.Fatalf("node 1 default next = %v", resp.Nodes[0].DefaultNextStepID)
	}
	if resp.Nodes[2].DefaultNextStepID != nil {
		t.Fatalf("the last step has no fall-through, got %v", *resp.Nodes[2].DefaultNextStepID)
	}
}

func TestGraphRejectsMissingCampaign(t *testing.T) {
	svc := NewService(&fakeStore{}, fakeChecker{err: errors.New("no rows")}, &fakeVariantStore{}, &fakeBranchStore{})
	if _, err := svc.Graph(context.Background(), uuid.New(), uuid.New()); !errors.Is(err, ErrCampaignNotFound) {
		t.Fatalf("want ErrCampaignNotFound, got %v", err)
	}
}

// A linear campaign (no routers) validates under every structural edit exactly
// as before branching existed: the check a delete/reorder now commits under
// never refuses a graph with no branches.
func TestCheckGraphAcceptsEveryLinearCampaign(t *testing.T) {
	var steps []gen.SequenceStep
	for i := range 5 {
		steps = append(steps, gen.SequenceStep{ID: uuid.New(), StepOrder: int32(i*2 + 1)}) // gaps too
	}
	if err := checkGraph(steps, nil); err != nil {
		t.Fatalf("linear campaign refused: %v", err)
	}
	if err := checkGraph(nil, nil); err != nil {
		t.Fatalf("empty campaign refused: %v", err)
	}
}
