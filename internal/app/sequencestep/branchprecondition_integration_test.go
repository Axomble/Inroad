//go:build integration

package sequencestep

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// wireToken is updated_at as a client holds it: serialized by the handler and
// parsed back the way the handler parses expected_updated_at. Every test goes
// through it, so a precision loss anywhere on the wire fails here rather than
// only in a browser.
func wireToken(t *testing.T, b gen.SequenceStepBranch) BranchPrecondition {
	t.Helper()
	at, err := parseExpectedUpdatedAt(toBranchResponse(b).UpdatedAt)
	if err != nil {
		t.Fatalf("updated_at %q does not parse back: %v", toBranchResponse(b).UpdatedAt, err)
	}
	return ExpectBranchAt(at)
}

func changedErr(t *testing.T, err error) *BranchChangedError {
	t.Helper()
	var changed *BranchChangedError
	if !errors.As(err, &changed) {
		t.Fatalf("want *BranchChangedError, got %v", err)
	}
	return changed
}

func (b branchIT) branchOf(t *testing.T, step uuid.UUID) *gen.SequenceStepBranch {
	t.Helper()
	g, err := b.svc.Graph(context.Background(), b.ws, b.campaign)
	if err != nil {
		t.Fatal(err)
	}
	for _, br := range g.Branches {
		if br.StepID == step {
			return &br
		}
	}
	return nil
}

func TestBranchPreconditionMatchingApplies(t *testing.T) {
	b, done := newBranchIT(t, "Precondition match")
	defer done()
	ctx := context.Background()

	created, err := b.svc.SetBranch(ctx, b.ws, b.campaign, BranchInput{
		StepID: b.steps[0], Condition: "always", YesStepID: &b.steps[2], Expect: ExpectNoBranch(),
	})
	if err != nil {
		t.Fatalf("create-if-absent on a step with no branch: %v", err)
	}
	replaced, err := b.svc.SetBranch(ctx, b.ws, b.campaign, BranchInput{
		StepID: b.steps[0], Condition: "always", Expect: wireToken(t, created),
	})
	if err != nil {
		t.Fatalf("replace at the current version: %v", err)
	}
	if replaced.YesStepID.Valid || !replaced.UpdatedAt.Time.After(created.UpdatedAt.Time) {
		t.Fatalf("replaced = %+v (was %v)", replaced, created.UpdatedAt.Time)
	}
	if err := b.svc.DeleteBranch(ctx, b.ws, b.campaign, b.steps[0], wireToken(t, replaced)); err != nil {
		t.Fatalf("delete at the current version: %v", err)
	}
	if got := b.branchOf(t, b.steps[0]); got != nil {
		t.Fatalf("branch survived its delete: %+v", got)
	}
}

// The core promise: a write made against a branch someone else has since
// changed is refused, writes nothing, and says what the branch is now.
func TestBranchPreconditionStaleIsRefusedWithCurrent(t *testing.T) {
	b, done := newBranchIT(t, "Precondition stale")
	defer done()
	ctx := context.Background()

	v1, err := b.svc.SetBranch(ctx, b.ws, b.campaign, BranchInput{StepID: b.steps[0], Condition: "always", YesStepID: &b.steps[2]})
	if err != nil {
		t.Fatal(err)
	}
	stale := wireToken(t, v1)
	// Another tab saves first.
	v2, err := b.svc.SetBranch(ctx, b.ws, b.campaign, BranchInput{StepID: b.steps[0], Condition: "always", Expect: stale})
	if err != nil {
		t.Fatal(err)
	}

	_, err = b.svc.SetBranch(ctx, b.ws, b.campaign, BranchInput{
		StepID: b.steps[0], Condition: "always", YesStepID: &b.steps[1], Expect: stale,
	})
	changed := changedErr(t, err)
	if changed.Current == nil || changed.Current.YesStepID.Valid || !changed.Current.UpdatedAt.Time.Equal(v2.UpdatedAt.Time) {
		t.Fatalf("current = %+v, want the second tab's save %+v", changed.Current, v2)
	}
	if got := b.branchOf(t, b.steps[0]); got == nil || got.YesStepID.Valid || !got.UpdatedAt.Time.Equal(v2.UpdatedAt.Time) {
		t.Fatalf("a refused write changed the branch: %+v", got)
	}

	// "Expect no branch" against a step that now has one is the same refusal.
	_, err = b.svc.SetBranch(ctx, b.ws, b.campaign, BranchInput{StepID: b.steps[0], Condition: "always", Expect: ExpectNoBranch()})
	if changed := changedErr(t, err); changed.Current == nil || changed.Current.StepID != b.steps[0] {
		t.Fatalf("current = %+v", changed.Current)
	}

	// Expecting a version of a branch that is gone reports current = nil.
	if err := b.svc.DeleteBranch(ctx, b.ws, b.campaign, b.steps[0], BranchPrecondition{}); err != nil {
		t.Fatal(err)
	}
	_, err = b.svc.SetBranch(ctx, b.ws, b.campaign, BranchInput{StepID: b.steps[0], Condition: "always", Expect: wireToken(t, v2)})
	if changed := changedErr(t, err); changed.Current != nil {
		t.Fatalf("current = %+v, want nil for a removed branch", changed.Current)
	}
	if got := b.branchOf(t, b.steps[0]); got != nil {
		t.Fatalf("an update-if-unchanged recreated a removed branch: %+v", got)
	}
}

// No precondition is exactly the old contract: last writer wins, delete is
// idempotent. API clients and agents that never send the field keep working.
func TestBranchWithoutPreconditionIsLastWriterWins(t *testing.T) {
	b, done := newBranchIT(t, "Precondition absent")
	defer done()
	ctx := context.Background()
	b.always(t, b.steps[0], &b.steps[2])
	b.always(t, b.steps[0], nil)
	if got := b.branchOf(t, b.steps[0]); got == nil || got.YesStepID.Valid {
		t.Fatalf("second unconditional write did not win: %+v", got)
	}
	for range 2 {
		if err := b.svc.DeleteBranch(ctx, b.ws, b.campaign, b.steps[0], BranchPrecondition{}); err != nil {
			t.Fatalf("unconditional delete: %v", err)
		}
	}
}

// Two editors create a branch on the same empty step at the same moment, each
// expecting none: exactly one wins, and the loser is told what the winner saved.
func TestBranchCreateIfAbsentRace(t *testing.T) {
	b, done := newBranchIT(t, "Precondition race")
	defer done()
	ctx := context.Background()

	targets := []*uuid.UUID{&b.steps[1], &b.steps[2]}
	rows := make([]gen.SequenceStepBranch, 2)
	errs := make([]error, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Go(func() {
			<-start
			rows[i], errs[i] = b.svc.SetBranch(ctx, b.ws, b.campaign, BranchInput{
				StepID: b.steps[0], Condition: "always", YesStepID: targets[i], Expect: ExpectNoBranch(),
			})
		})
	}
	close(start)
	wg.Wait()

	winner, loser := 0, 1
	if errs[0] != nil {
		winner, loser = 1, 0
	}
	if errs[winner] != nil {
		t.Fatalf("both creates failed: %v / %v", errs[0], errs[1])
	}
	changed := changedErr(t, errs[loser])
	if changed.Current == nil || changed.Current.YesStepID.Bytes != *targets[winner] {
		t.Fatalf("loser's current = %+v, want the winner's branch", changed.Current)
	}
	if got := b.branchOf(t, b.steps[0]); got == nil || got.YesStepID.Bytes != *targets[winner] ||
		!got.UpdatedAt.Time.Equal(rows[winner].UpdatedAt.Time) {
		t.Fatalf("stored = %+v, want the winner's %+v", got, rows[winner])
	}
}

func TestBranchDeleteWithStalePreconditionIsRefused(t *testing.T) {
	b, done := newBranchIT(t, "Precondition delete")
	defer done()
	ctx := context.Background()
	v1, err := b.svc.SetBranch(ctx, b.ws, b.campaign, BranchInput{StepID: b.steps[0], Condition: "always", YesStepID: &b.steps[2]})
	if err != nil {
		t.Fatal(err)
	}
	stale := wireToken(t, v1)
	v2, err := b.svc.SetBranch(ctx, b.ws, b.campaign, BranchInput{StepID: b.steps[0], Condition: "always", Expect: stale})
	if err != nil {
		t.Fatal(err)
	}

	changed := changedErr(t, b.svc.DeleteBranch(ctx, b.ws, b.campaign, b.steps[0], stale))
	if changed.Current == nil || !changed.Current.UpdatedAt.Time.Equal(v2.UpdatedAt.Time) {
		t.Fatalf("current = %+v, want %+v", changed.Current, v2)
	}
	if b.branchOf(t, b.steps[0]) == nil {
		t.Fatal("a refused delete removed the branch")
	}

	// Once someone else has removed it, a delete at the old version is refused
	// with current = nil: the client learns it is gone rather than being told
	// its own delete succeeded.
	if err := b.svc.DeleteBranch(ctx, b.ws, b.campaign, b.steps[0], wireToken(t, v2)); err != nil {
		t.Fatal(err)
	}
	if changed := changedErr(t, b.svc.DeleteBranch(ctx, b.ws, b.campaign, b.steps[0], wireToken(t, v2))); changed.Current != nil {
		t.Fatalf("current = %+v, want nil", changed.Current)
	}
}

// A precondition changes nothing about tenancy: a foreign workspace is 404
// whatever it expects, through the service or the store directly, and a
// precondition failure never leaks the owner's branch to it.
func TestBranchPreconditionStaysWorkspacePinned(t *testing.T) {
	b, done := newBranchIT(t, "Precondition tenant owner")
	defer done()
	ctx := context.Background()
	owned, err := b.svc.SetBranch(ctx, b.ws, b.campaign, BranchInput{StepID: b.steps[0], Condition: "always"})
	if err != nil {
		t.Fatal(err)
	}
	intruder, err := b.q.CreateWorkspace(ctx, "Precondition tenant intruder "+uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	store := NewPgBranchStore(b.pool)
	for name, expect := range map[string]BranchPrecondition{
		"none": {}, "absent": ExpectNoBranch(), "at": wireToken(t, owned),
	} {
		in := BranchInput{CampaignID: b.campaign, StepID: b.steps[0], Condition: "always", YesStepID: &b.steps[2], Expect: expect}
		if _, err := b.svc.SetBranch(ctx, intruder.ID, b.campaign, in); !errors.Is(err, ErrCampaignNotFound) {
			t.Errorf("%s: service set: want ErrCampaignNotFound, got %v", name, err)
		}
		if _, err := store.UpsertBranch(ctx, intruder.ID, in, checkGraph); !errors.Is(err, ErrCampaignNotFound) {
			t.Errorf("%s: store set: want ErrCampaignNotFound, got %v", name, err)
		}
		if err := b.svc.DeleteBranch(ctx, intruder.ID, b.campaign, b.steps[0], expect); !errors.Is(err, ErrCampaignNotFound) {
			t.Errorf("%s: service delete: want ErrCampaignNotFound, got %v", name, err)
		}
		if err := store.DeleteBranch(ctx, intruder.ID, b.campaign, b.steps[0], expect, checkGraph); !errors.Is(err, ErrCampaignNotFound) {
			t.Errorf("%s: store delete: want ErrCampaignNotFound, got %v", name, err)
		}
	}
	if got := b.branchOf(t, b.steps[0]); got == nil || got.YesStepID.Valid || !got.UpdatedAt.Time.Equal(owned.UpdatedAt.Time) {
		t.Fatalf("the intruder changed the owner's branch: %+v", got)
	}
}

// updated_at is a token only if every write moves it. Back-to-back writes in
// one tight loop (well inside a millisecond each on a local database) must each
// see a strictly later value, and so must the ON DELETE SET NULL that deleting
// an exit's target step performs — a write no query of ours makes.
func TestBranchUpdatedAtAdvancesOnEveryWrite(t *testing.T) {
	b, done := newBranchIT(t, "Precondition advances")
	defer done()
	ctx := context.Background()

	prev, err := b.svc.SetBranch(ctx, b.ws, b.campaign, BranchInput{StepID: b.steps[0], Condition: "always", YesStepID: &b.steps[2]})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 20 {
		next, err := b.svc.SetBranch(ctx, b.ws, b.campaign, BranchInput{StepID: b.steps[0], Condition: "always", YesStepID: &b.steps[2]})
		if err != nil {
			t.Fatal(err)
		}
		if !next.UpdatedAt.Time.After(prev.UpdatedAt.Time) {
			t.Fatalf("write %d: updated_at %v did not advance past %v", i, next.UpdatedAt.Time, prev.UpdatedAt.Time)
		}
		prev = next
	}

	// Same-transaction rewrites share now(); the trigger must still advance.
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var first, second time.Time
	for _, dst := range []*time.Time{&first, &second} {
		if err := tx.QueryRow(ctx, `UPDATE sequence_step_branches SET condition = 'always' WHERE step_id = $1 RETURNING updated_at`, b.steps[0]).Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	if !second.After(first) || !first.After(prev.UpdatedAt.Time) {
		t.Fatalf("in one transaction: %v then %v (after %v)", first, second, prev.UpdatedAt.Time)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	if err := b.svc.Delete(ctx, b.ws, b.campaign, b.steps[2]); err != nil {
		t.Fatalf("delete target step: %v", err)
	}
	after := b.branchOf(t, b.steps[0])
	if after == nil || after.YesStepID.Valid || !after.UpdatedAt.Time.After(prev.UpdatedAt.Time) {
		t.Fatalf("after its exit's target was deleted: %+v (was %v)", after, prev.UpdatedAt.Time)
	}
	// So a client holding the pre-delete token is told the branch changed.
	_, err = b.svc.SetBranch(ctx, b.ws, b.campaign, BranchInput{StepID: b.steps[0], Condition: "always", Expect: wireToken(t, prev)})
	changedErr(t, err)
}
