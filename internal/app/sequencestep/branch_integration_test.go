//go:build integration

package sequencestep

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/dbtest"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/seqgraph"
)

// sqlChecker is the CampaignChecker the composition root builds over the
// campaign store, reduced to the one workspace-pinned read it needs.
type sqlChecker struct{ pool *pgxpool.Pool }

func (c sqlChecker) CampaignStatus(ctx context.Context, ws, campaignID uuid.UUID) (string, error) {
	var status string
	err := c.pool.QueryRow(ctx, `SELECT status FROM campaigns WHERE id = $1 AND workspace_id = $2`, campaignID, ws).Scan(&status)
	return status, err
}

type branchIT struct {
	pool     *pgxpool.Pool
	q        *gen.Queries
	svc      *Service
	ws       uuid.UUID
	campaign uuid.UUID
	steps    []uuid.UUID
}

func newBranchIT(t *testing.T, label string) (branchIT, func()) {
	t.Helper()
	ctx := context.Background()
	if err := db.Migrate(dbtest.DSN(t)); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := db.Connect(ctx, dbtest.DSN(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	q := gen.New(pool)
	ws, campaign, ids := seedThreeSteps(t, ctx, q, label)
	svc := NewService(NewPgStore(pool), sqlChecker{pool: pool}, NewPgVariantStore(q), NewPgBranchStore(pool))
	return branchIT{pool: pool, q: q, svc: svc, ws: ws, campaign: campaign, steps: ids}, pool.Close
}

func (b branchIT) addStep(t *testing.T, order int32) uuid.UUID {
	t.Helper()
	st, err := b.q.CreateStep(context.Background(), gen.CreateStepParams{
		WorkspaceID: b.ws, CampaignID: b.campaign, StepOrder: order, Subject: "s", BodyText: "b",
	})
	if err != nil {
		t.Fatalf("step %d: %v", order, err)
	}
	return st.ID
}

func (b branchIT) always(t *testing.T, from uuid.UUID, to *uuid.UUID) {
	t.Helper()
	if _, err := b.svc.SetBranch(context.Background(), b.ws, b.campaign, BranchInput{StepID: from, Condition: "always", YesStepID: to}); err != nil {
		t.Fatalf("always %v -> %v: %v", from, to, err)
	}
}

func TestBranchSaveAndReadBack(t *testing.T) {
	b, done := newBranchIT(t, "Branch save")
	defer done()
	ctx := context.Background()
	three := int32(3)
	label := "positive" // seeded for every workspace by migration 000047
	got, err := b.svc.SetBranch(ctx, b.ws, b.campaign, BranchInput{
		StepID: b.steps[0], Condition: "replied", WithinDays: &three, ReplyLabelKey: &label,
		YesStepID: &b.steps[2], NoStepID: &b.steps[1],
	})
	if err != nil {
		t.Fatalf("SetBranch: %v", err)
	}
	if got.WorkspaceID != b.ws || got.CampaignID != b.campaign || got.ReplyLabelKey == nil || *got.ReplyLabelKey != label {
		t.Fatalf("stored = %+v", got)
	}

	// Replace in place: one row per step.
	if _, err := b.svc.SetBranch(ctx, b.ws, b.campaign, BranchInput{StepID: b.steps[0], Condition: "always"}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	g, err := b.svc.Graph(ctx, b.ws, b.campaign)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Branches) != 1 || g.Branches[0].Condition != "always" || g.Branches[0].WithinDays != nil || g.Branches[0].YesStepID.Valid {
		t.Fatalf("graph branches = %+v", g.Branches)
	}

	if err := b.svc.DeleteBranch(ctx, b.ws, b.campaign, b.steps[0]); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if g, _ := b.svc.Graph(ctx, b.ws, b.campaign); len(g.Branches) != 0 {
		t.Fatalf("branch survived delete: %+v", g.Branches)
	}
}

func TestBranchCycleRefusedAndNothingWritten(t *testing.T) {
	b, done := newBranchIT(t, "Branch cycle")
	defer done()
	_, err := b.svc.SetBranch(context.Background(), b.ws, b.campaign, BranchInput{
		StepID: b.steps[2], Condition: "always", YesStepID: &b.steps[0],
	})
	var cyc *seqgraph.CycleError
	if !errors.As(err, &cyc) {
		t.Fatalf("want CycleError, got %v", err)
	}
	if g, _ := b.svc.Graph(context.Background(), b.ws, b.campaign); len(g.Branches) != 0 {
		t.Fatalf("a refused edit was committed: %+v", g.Branches)
	}
}

// A target in ANOTHER campaign of the same workspace is refused by the service
// with a named error, and by the composite FK if the service is bypassed.
func TestBranchTargetMustBeInSameCampaign(t *testing.T) {
	b, done := newBranchIT(t, "Branch target owner")
	defer done()
	ctx := context.Background()
	_, _, others := seedThreeSteps(t, ctx, b.q, "Branch target other")

	_, err := b.svc.SetBranch(ctx, b.ws, b.campaign, BranchInput{StepID: b.steps[0], Condition: "always", YesStepID: &others[0]})
	var te *seqgraph.TargetError
	if !errors.As(err, &te) {
		t.Fatalf("service: want TargetError, got %v", err)
	}

	store := NewPgBranchStore(b.pool)
	_, err = store.UpsertBranch(ctx, b.ws, BranchInput{
		CampaignID: b.campaign, StepID: b.steps[0], Condition: "always", YesStepID: &others[0],
	}, checkGraph)
	if !errors.Is(err, ErrTargetGone) {
		t.Fatalf("store backstop: want ErrTargetGone (composite FK), got %v", err)
	}
}

// Every graph write takes the campaign lock on (campaign, workspace) first, so
// a foreign workspace writes nothing — even through the store directly.
func TestBranchWritesAreWorkspacePinned(t *testing.T) {
	b, done := newBranchIT(t, "Branch tenant owner")
	defer done()
	ctx := context.Background()
	intruder, err := b.q.CreateWorkspace(ctx, "Branch tenant intruder "+uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}

	if _, err := b.svc.SetBranch(ctx, intruder.ID, b.campaign, BranchInput{StepID: b.steps[0], Condition: "always"}); !errors.Is(err, ErrCampaignNotFound) {
		t.Fatalf("service: want ErrCampaignNotFound, got %v", err)
	}
	store := NewPgBranchStore(b.pool)
	if _, err := store.UpsertBranch(ctx, intruder.ID, BranchInput{CampaignID: b.campaign, StepID: b.steps[0], Condition: "always"}, checkGraph); !errors.Is(err, ErrCampaignNotFound) {
		t.Fatalf("store: want ErrCampaignNotFound, got %v", err)
	}
	if err := store.DeleteBranch(ctx, intruder.ID, b.campaign, b.steps[0], checkGraph); !errors.Is(err, ErrCampaignNotFound) {
		t.Fatalf("store delete: want ErrCampaignNotFound, got %v", err)
	}
	if rows, _ := b.q.ListBranchesByCampaign(ctx, gen.ListBranchesByCampaignParams{CampaignID: b.campaign, WorkspaceID: intruder.ID}); len(rows) != 0 {
		t.Fatalf("foreign workspace sees %d branches", len(rows))
	}
	if g, _ := b.svc.Graph(ctx, b.ws, b.campaign); len(g.Branches) != 0 {
		t.Fatalf("intruder wrote into the owner's graph: %+v", g.Branches)
	}
}

// Deleting a target step turns the exits that pointed at it into ends (ON
// DELETE SET NULL on the target column only), and deleting a SOURCE step
// removes its router.
func TestStepDeleteNullsExitsAndDropsRouter(t *testing.T) {
	b, done := newBranchIT(t, "Branch step delete")
	defer done()
	ctx := context.Background()
	two := int32(2)
	if _, err := b.svc.SetBranch(ctx, b.ws, b.campaign, BranchInput{
		StepID: b.steps[0], Condition: "opened", WithinDays: &two, YesStepID: &b.steps[2], NoStepID: &b.steps[1],
	}); err != nil {
		t.Fatal(err)
	}
	b.always(t, b.steps[1], nil)

	if err := b.svc.Delete(ctx, b.ws, b.campaign, b.steps[2]); err != nil {
		t.Fatalf("delete target: %v", err)
	}
	g, err := b.svc.Graph(ctx, b.ws, b.campaign)
	if err != nil {
		t.Fatal(err)
	}
	for _, br := range g.Branches {
		if br.StepID == b.steps[0] {
			if br.YesStepID.Valid || !br.NoStepID.Valid || br.CampaignID != b.campaign {
				t.Fatalf("after target delete = %+v, want yes nulled, no kept", br)
			}
		}
	}

	if err := b.svc.Delete(ctx, b.ws, b.campaign, b.steps[1]); err != nil {
		t.Fatalf("delete source: %v", err)
	}
	g, _ = b.svc.Graph(ctx, b.ws, b.campaign)
	if len(g.Branches) != 1 || g.Branches[0].StepID != b.steps[0] || g.Branches[0].NoStepID.Valid {
		t.Fatalf("after source delete = %+v", g.Branches)
	}
}

// A delete re-links the fall-through around the removed step. Here A -> B (by
// order), B -> D, C -> A, D ends: acyclic. Deleting B makes A fall through to C,
// and C -> A closes a loop, so the delete is refused and rolled back.
func TestStepDeleteRefusedWhenFallThroughClosesLoop(t *testing.T) {
	b, done := newBranchIT(t, "Branch delete cycle")
	defer done()
	ctx := context.Background()
	a, bb, c := b.steps[0], b.steps[1], b.steps[2]
	d := b.addStep(t, 4)
	b.always(t, bb, &d)
	b.always(t, c, &a)

	err := b.svc.Delete(ctx, b.ws, b.campaign, bb)
	if seqgraph.CodeOf(err) != seqgraph.CodeCycle {
		t.Fatalf("want a cycle refusal, got %v", err)
	}
	if _, err := b.q.GetStep(ctx, gen.GetStepParams{ID: bb, WorkspaceID: b.ws}); err != nil {
		t.Fatalf("the refused delete must be rolled back: %v", err)
	}
}

// Reordering moves every fall-through edge. A -> C explicitly with B and C
// falling through is acyclic; the order [C, B, A] makes C -> B -> A by order,
// and A -> C closes the loop.
func TestReorderRefusedWhenFallThroughClosesLoop(t *testing.T) {
	b, done := newBranchIT(t, "Branch reorder cycle")
	defer done()
	ctx := context.Background()
	a, bb, c := b.steps[0], b.steps[1], b.steps[2]
	b.always(t, a, &c)

	_, err := b.svc.Reorder(ctx, b.ws, b.campaign, []uuid.UUID{c, bb, a})
	if seqgraph.CodeOf(err) != seqgraph.CodeCycle {
		t.Fatalf("want a cycle refusal, got %v", err)
	}
	after, err := b.q.ListStepsByCampaign(ctx, gen.ListStepsByCampaignParams{CampaignID: b.campaign, WorkspaceID: b.ws})
	if err != nil {
		t.Fatal(err)
	}
	if got := orderedIDs(after); !equalIDs(got, b.steps) {
		t.Fatalf("refused reorder was committed: %v", got)
	}
}

// The composite FKs make a cross-campaign reference unrepresentable even for a
// raw write that skips every Go check.
func TestBranchSchemaRefusesForeignReferences(t *testing.T) {
	b, done := newBranchIT(t, "Branch schema owner")
	defer done()
	ctx := context.Background()
	otherWS, otherCampaign, others := seedThreeSteps(t, ctx, b.q, "Branch schema other")

	cases := map[string]struct {
		ws, campaign, step uuid.UUID
		yes                *uuid.UUID
	}{
		"source step of another campaign": {b.ws, b.campaign, others[0], nil},
		"campaign of another workspace":   {b.ws, otherCampaign, others[0], nil},
		"target in another campaign":      {b.ws, b.campaign, b.steps[0], &others[1]},
		"workspace mismatching campaign":  {otherWS, b.campaign, b.steps[0], nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := b.q.UpsertBranch(ctx, gen.UpsertBranchParams{
				StepID: tc.step, WorkspaceID: tc.ws, CampaignID: tc.campaign, Condition: "always",
				YesStepID: nullUUID(tc.yes),
			})
			if err == nil {
				t.Fatal("the schema accepted a foreign reference")
			}
		})
	}
}
