package sequencestep

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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
	// labels maps a label key to whether it stops the enrollment; a missing key
	// is a label that does not exist.
	labels map[string]bool
	// trackingOff models a campaign with tracking disabled.
	trackingOff bool
	upserts     int
	// expects records every precondition the store was handed, in call order.
	expects []BranchPrecondition
}

func (f *fakeBranchStore) ListBranches(context.Context, uuid.UUID, uuid.UUID) ([]gen.SequenceStepBranch, error) {
	return f.list(), nil
}

func (f *fakeBranchStore) ReplyLabelStops(_ context.Context, _ uuid.UUID, key string) (stops, found bool, err error) {
	stops, found = f.labels[key]
	return stops, found, nil
}

func (f *fakeBranchStore) TrackingEnabled(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return !f.trackingOff, nil
}

func (f *fakeBranchStore) UpsertBranch(_ context.Context, ws uuid.UUID, in BranchInput, check GraphCheck) (gen.SequenceStepBranch, error) {
	f.expects = append(f.expects, in.Expect)
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

func (f *fakeBranchStore) DeleteBranch(_ context.Context, _, _, stepID uuid.UUID, expect BranchPrecondition, check GraphCheck) error {
	f.expects = append(f.expects, expect)
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
	variants *fakeVariantStore
	steps    *stepsByID
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
		steps = append(steps, gen.SequenceStep{ID: id, CampaignID: campaign, StepOrder: int32(i + 1), BodyHtml: "<p>hi</p>"})
	}
	// "positive" is a builtin label (stops the enrollment); "soft_yes" is a
	// custom one that does not.
	bs := &fakeBranchStore{steps: steps, labels: map[string]bool{"positive": true, "soft_yes": false}}
	store := &stepsByID{steps: steps}
	vs := &fakeVariantStore{}
	return branchFixture{
		svc:      NewService(store, fakeChecker{status: status}, vs, bs),
		branches: bs, variants: vs, steps: store, campaign: campaign, ws: uuid.New(), step: ids,
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
	svc := NewService(&fakeStore{}, fakeChecker{err: pgx.ErrNoRows}, &fakeVariantStore{}, f.branches)
	_, err := svc.SetBranch(context.Background(), f.ws, f.campaign, BranchInput{StepID: f.step[0], Condition: "always"})
	if !errors.Is(err, ErrCampaignNotFound) {
		t.Fatalf("want ErrCampaignNotFound, got %v", err)
	}
}

// Only a genuine miss is a 404. A database failure looking the campaign up is
// a server error, and all three graph endpoints must say so rather than
// telling the client the campaign does not exist.
func TestGraphEndpointsDoNotReportADatabaseErrorAsNotFound(t *testing.T) {
	f := newBranchFixture(t, "draft")
	boom := errors.New("connection reset")
	svc := NewService(f.steps, fakeChecker{err: boom}, &fakeVariantStore{}, f.branches)
	ctx := context.Background()
	_, gerr := svc.Graph(ctx, f.ws, f.campaign)
	_, serr := svc.SetBranch(ctx, f.ws, f.campaign, BranchInput{StepID: f.step[0], Condition: "always"})
	derr := svc.DeleteBranch(ctx, f.ws, f.campaign, f.step[0], BranchPrecondition{})
	for name, err := range map[string]error{"Graph": gerr, "SetBranch": serr, "DeleteBranch": derr} {
		if !errors.Is(err, boom) || errors.Is(err, ErrCampaignNotFound) {
			t.Errorf("%s: got %v, want the wrapped database error", name, err)
		}
	}
}

// A reply condition may name a label only if replies with it leave the
// enrollment running: a stopping label (every builtin human one) ends the
// sequence before any branch is consulted, so the branch could never fire.
func TestSetBranchReplyLabelMustNotStopTheSequence(t *testing.T) {
	ctx := context.Background()
	f := newBranchFixture(t, "running")
	_, err := f.svc.SetBranch(ctx, f.ws, f.campaign, BranchInput{
		StepID: f.step[0], Condition: "replied", WithinDays: ptr(int32(2)), ReplyLabelKey: ptr("positive"), YesStepID: &f.step[2],
	})
	if seqgraph.CodeOf(err) != seqgraph.CodeLabelStopsSequence {
		t.Fatalf("stopping label: code %q (%v), want %q", seqgraph.CodeOf(err), err, seqgraph.CodeLabelStopsSequence)
	}
	if _, err := f.svc.SetBranch(ctx, f.ws, f.campaign, BranchInput{
		StepID: f.step[0], Condition: "not_replied", WithinDays: ptr(int32(2)), ReplyLabelKey: ptr("soft_yes"), YesStepID: &f.step[2],
	}); err != nil {
		t.Fatalf("a non-stopping label is routable: %v", err)
	}
}

// Open and click conditions need somewhere for an open or click to be recorded:
// tracking on for the campaign, and an HTML body on the step and every variant.
// Reply conditions need neither.
func TestSetBranchOpenClickNeedTracking(t *testing.T) {
	ctx := context.Background()
	openIn := func(f branchFixture, cond string) BranchInput {
		return BranchInput{StepID: f.step[0], Condition: cond, WithinDays: ptr(int32(1)), YesStepID: &f.step[1]}
	}

	f := newBranchFixture(t, "running")
	f.branches.trackingOff = true
	for _, cond := range []string{"opened", "clicked", "not_opened"} {
		if _, err := f.svc.SetBranch(ctx, f.ws, f.campaign, openIn(f, cond)); seqgraph.CodeOf(err) != seqgraph.CodeTrackingRequired {
			t.Errorf("%s with tracking off: %v", cond, err)
		}
	}
	if _, err := f.svc.SetBranch(ctx, f.ws, f.campaign, openIn(f, "replied")); err != nil {
		t.Errorf("replied does not need tracking: %v", err)
	}

	f = newBranchFixture(t, "running")
	f.steps.steps[0].BodyHtml = ""
	if _, err := f.svc.SetBranch(ctx, f.ws, f.campaign, openIn(f, "opened")); seqgraph.CodeOf(err) != seqgraph.CodeTrackingRequired {
		t.Errorf("text-only step: %v", err)
	}

	f = newBranchFixture(t, "running")
	f.variants.variants = []Variant{{ID: uuid.New(), StepID: f.step[0], BodyHTML: "<p>b</p>"}, {ID: uuid.New(), StepID: f.step[0]}}
	if _, err := f.svc.SetBranch(ctx, f.ws, f.campaign, openIn(f, "clicked")); seqgraph.CodeOf(err) != seqgraph.CodeTrackingRequired {
		t.Errorf("a text-only variant: %v", err)
	}
	if f.branches.upserts != 0 {
		t.Fatal("a refused open/click branch must not be written")
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
		"label that stops the sequence": {func(f branchFixture) BranchInput {
			return BranchInput{StepID: f.step[0], Condition: "replied", WithinDays: ptr(int32(1)), ReplyLabelKey: ptr("positive")}
		}, seqgraph.CodeLabelStopsSequence},
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
	err := f.svc.DeleteBranch(ctx, f.ws, f.campaign, f.step[0], BranchPrecondition{})
	if seqgraph.CodeOf(err) != seqgraph.CodeCycle {
		t.Fatalf("want a cycle refusal, got %v", err)
	}
	if _, still := f.branches.branches[f.step[0]]; !still {
		t.Fatal("the refused delete must be rolled back")
	}
}

func TestDeleteBranchIsIdempotent(t *testing.T) {
	f := newBranchFixture(t, "running")
	if err := f.svc.DeleteBranch(context.Background(), f.ws, f.campaign, f.step[0], BranchPrecondition{}); err != nil {
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
	svc := NewService(&fakeStore{}, fakeChecker{err: pgx.ErrNoRows}, &fakeVariantStore{}, &fakeBranchStore{})
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

// The precondition is enforced by the store, in the write; the service's job is
// to hand it over untouched on both writes, and to refuse a malformed branch
// before any precondition is consulted.
func TestBranchPreconditionReachesTheStore(t *testing.T) {
	ctx := context.Background()
	f := newBranchFixture(t, "running")
	at := time.Date(2026, 9, 23, 16, 10, 44, 123456000, time.UTC)

	if _, err := f.svc.SetBranch(ctx, f.ws, f.campaign, BranchInput{
		StepID: f.step[0], Condition: "always", Expect: ExpectNoBranch(),
	}); err != nil {
		t.Fatalf("SetBranch: %v", err)
	}
	if err := f.svc.DeleteBranch(ctx, f.ws, f.campaign, f.step[0], ExpectBranchAt(at)); err != nil {
		t.Fatalf("DeleteBranch: %v", err)
	}
	want := []BranchPrecondition{ExpectNoBranch(), ExpectBranchAt(at)}
	if !slices.Equal(f.branches.expects, want) {
		t.Fatalf("store saw %+v, want %+v", f.branches.expects, want)
	}

	f = newBranchFixture(t, "running")
	if _, err := f.svc.SetBranch(ctx, f.ws, f.campaign, BranchInput{
		StepID: f.step[0], Condition: "bounced", Expect: ExpectBranchAt(at),
	}); seqgraph.CodeOf(err) != seqgraph.CodeInvalidCondition {
		t.Fatalf("a malformed branch is refused as malformed, got %v", err)
	}
	if len(f.branches.expects) != 0 {
		t.Fatal("a malformed branch must not reach the store")
	}
}
