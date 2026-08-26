//go:build integration

package inbox_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/inbox"
)

// Labels against real Postgres. The rules worth verifying here are the ones
// only the database enforces: the case-insensitive unique index (a lower(name)
// EXPRESSION index, which is exactly the kind of thing a Go fake cannot prove),
// the join's composite primary key making assignment idempotent, and both
// cascade directions.

func TestLabelCrudAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)

	created, err := f.store.CreateLabel(ctx, f.ws, "Invoices", "#3b82f6")
	if err != nil {
		t.Fatalf("CreateLabel: %v", err)
	}
	if created.Name != "Invoices" || created.Color != "#3b82f6" {
		t.Errorf("created = %+v", created)
	}

	got, err := f.store.GetLabel(ctx, f.ws, created.ID)
	if err != nil {
		t.Fatalf("GetLabel: %v", err)
	}
	if got.ID != created.ID {
		t.Errorf("GetLabel returned %s, want %s", got.ID, created.ID)
	}

	updated, err := f.store.UpdateLabel(ctx, f.ws, created.ID, "Billing", "#ef4444")
	if err != nil {
		t.Fatalf("UpdateLabel: %v", err)
	}
	if updated.Name != "Billing" || updated.Color != "#ef4444" {
		t.Errorf("updated = %+v", updated)
	}
	if !updated.UpdatedAt.After(created.UpdatedAt) && !updated.UpdatedAt.Equal(created.UpdatedAt) {
		t.Errorf("updated_at went backwards: %v -> %v", created.UpdatedAt, updated.UpdatedAt)
	}

	if err := f.store.DeleteLabel(ctx, f.ws, created.ID); err != nil {
		t.Fatalf("DeleteLabel: %v", err)
	}
	if _, err := f.store.GetLabel(ctx, f.ws, created.ID); !errors.Is(err, inbox.ErrNotFound) {
		t.Errorf("GetLabel after delete = %v, want ErrNotFound", err)
	}
	if err := f.store.DeleteLabel(ctx, f.ws, created.ID); !errors.Is(err, inbox.ErrNotFound) {
		t.Errorf("second DeleteLabel = %v, want ErrNotFound", err)
	}
}

// The unique index is on lower(name), an EXPRESSION index — the one rule a Go
// fake's strings.EqualFold cannot actually prove.
func TestLabelNamesAreUniqueCaseInsensitivelyAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)

	if _, err := f.store.CreateLabel(ctx, f.ws, "Invoices", "#3b82f6"); err != nil {
		t.Fatalf("CreateLabel: %v", err)
	}
	for _, name := range []string{"Invoices", "invoices", "INVOICES", "InVoIcEs"} {
		_, err := f.store.CreateLabel(ctx, f.ws, name, "#3b82f6")
		if !errors.Is(err, inbox.ErrLabelNameTaken) {
			t.Errorf("CreateLabel(%q) = %v, want ErrLabelNameTaken", name, err)
		}
	}

	// FindLabelByName must match the same way, or the search-or-create path
	// would fail to recover from the conflict it just caused.
	found, err := f.store.FindLabelByName(ctx, f.ws, "INVOICES")
	if err != nil {
		t.Fatalf("FindLabelByName(differing case): %v", err)
	}
	if found.Name != "Invoices" {
		t.Errorf("found %q, want the stored %q", found.Name, "Invoices")
	}
}

// Uniqueness is per workspace, so the same name in two workspaces is two
// labels, not a clash.
func TestLabelNameUniquenessIsPerWorkspaceAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	foreignWS, _ := seedTenant(t, ctx, f.q)

	if _, err := f.store.CreateLabel(ctx, f.ws, "Shared", "#3b82f6"); err != nil {
		t.Fatalf("CreateLabel(ws1): %v", err)
	}
	if _, err := f.store.CreateLabel(ctx, foreignWS, "Shared", "#3b82f6"); err != nil {
		t.Fatalf("the same name in another workspace was rejected: %v", err)
	}

	mine, err := f.store.ListLabels(ctx, f.ws)
	if err != nil {
		t.Fatalf("ListLabels: %v", err)
	}
	if len(mine) != 1 {
		t.Errorf("ListLabels = %d labels, want 1 — another workspace's leaked in", len(mine))
	}
}

// Invariant: never read or write another workspace's row (docs/security.md).
func TestLabelsAreWorkspaceScopedAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	foreignWS, _ := seedTenant(t, ctx, f.q)

	label, err := f.store.CreateLabel(ctx, f.ws, "Invoices", "#3b82f6")
	if err != nil {
		t.Fatalf("CreateLabel: %v", err)
	}

	if _, err := f.store.GetLabel(ctx, foreignWS, label.ID); !errors.Is(err, inbox.ErrNotFound) {
		t.Errorf("GetLabel(foreign) = %v, want ErrNotFound", err)
	}
	if _, err := f.store.UpdateLabel(ctx, foreignWS, label.ID, "Hijacked", "#000000"); !errors.Is(err, inbox.ErrNotFound) {
		t.Errorf("UpdateLabel(foreign) = %v, want ErrNotFound", err)
	}
	if err := f.store.DeleteLabel(ctx, foreignWS, label.ID); !errors.Is(err, inbox.ErrNotFound) {
		t.Errorf("DeleteLabel(foreign) = %v, want ErrNotFound", err)
	}

	// ...and the real owner's label survived all three attempts, unmodified.
	still, err := f.store.GetLabel(ctx, f.ws, label.ID)
	if err != nil {
		t.Fatalf("the owner's label did not survive: %v", err)
	}
	if still.Name != "Invoices" {
		t.Errorf("Name = %q, want the untouched %q", still.Name, "Invoices")
	}
}

// The composite PK is what makes assignment idempotent — ON CONFLICT DO
// NOTHING relies on it, and a second assign must add no row.
func TestAssignLabelIsIdempotentAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	th := recordInbound(t, ctx, f, "<label-idem@s.test>", time.Now().UTC())
	label, err := f.store.CreateLabel(ctx, f.ws, "Invoices", "#3b82f6")
	if err != nil {
		t.Fatalf("CreateLabel: %v", err)
	}

	for range 3 {
		if err := f.store.AssignLabel(ctx, f.ws, th.ID, label.ID); err != nil {
			t.Fatalf("AssignLabel: %v", err)
		}
	}

	var rows int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM inbox_thread_labels WHERE thread_id = $1 AND label_id = $2`,
		th.ID, label.ID).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 1 {
		t.Errorf("%d join rows after three assigns, want 1", rows)
	}

	labels, err := f.store.LabelsForThread(ctx, f.ws, th.ID)
	if err != nil {
		t.Fatalf("LabelsForThread: %v", err)
	}
	if len(labels) != 1 {
		t.Errorf("thread carries %d labels, want 1", len(labels))
	}

	if err := f.store.UnassignLabel(ctx, f.ws, th.ID, label.ID); err != nil {
		t.Fatalf("UnassignLabel: %v", err)
	}
	if err := f.store.UnassignLabel(ctx, f.ws, th.ID, label.ID); !errors.Is(err, inbox.ErrNotFound) {
		t.Errorf("second UnassignLabel = %v, want ErrNotFound", err)
	}
}

// The list view's one-query-per-page path, which exists to avoid an N+1.
func TestLabelsForThreadsBatchesAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	now := time.Now().UTC()
	a := recordInbound(t, ctx, f, "<label-batch-a@s.test>", now)
	b := recordInbound(t, ctx, f, "<label-batch-b@s.test>", now.Add(-time.Minute))
	unlabelled := recordInbound(t, ctx, f, "<label-batch-c@s.test>", now.Add(-2*time.Minute))

	// Deliberately created out of alphabetical order, to prove the query sorts.
	zulu, err := f.store.CreateLabel(ctx, f.ws, "Zulu", "#3b82f6")
	if err != nil {
		t.Fatalf("CreateLabel(Zulu): %v", err)
	}
	alpha, err := f.store.CreateLabel(ctx, f.ws, "alpha", "#ef4444")
	if err != nil {
		t.Fatalf("CreateLabel(alpha): %v", err)
	}

	for _, labelID := range []uuid.UUID{zulu.ID, alpha.ID} {
		if err := f.store.AssignLabel(ctx, f.ws, a.ID, labelID); err != nil {
			t.Fatalf("AssignLabel: %v", err)
		}
	}
	if err := f.store.AssignLabel(ctx, f.ws, b.ID, zulu.ID); err != nil {
		t.Fatalf("AssignLabel(b): %v", err)
	}

	byThread, err := f.store.LabelsForThreads(ctx, f.ws, []uuid.UUID{a.ID, b.ID, unlabelled.ID})
	if err != nil {
		t.Fatalf("LabelsForThreads: %v", err)
	}
	if len(byThread[a.ID]) != 2 {
		t.Errorf("thread a has %d labels, want 2", len(byThread[a.ID]))
	}
	// Case-insensitive alphabetical: "alpha" before "Zulu", which a raw byte
	// ordering would reverse.
	if len(byThread[a.ID]) == 2 && byThread[a.ID][0].Name != "alpha" {
		t.Errorf("thread a's labels = %q, want alpha first", []string{byThread[a.ID][0].Name, byThread[a.ID][1].Name})
	}
	if len(byThread[b.ID]) != 1 {
		t.Errorf("thread b has %d labels, want 1", len(byThread[b.ID]))
	}
	if _, present := byThread[unlabelled.ID]; present {
		t.Error("an unlabelled thread appeared in the batch result")
	}

	// An empty page must not issue a query at all — and must not error.
	empty, err := f.store.LabelsForThreads(ctx, f.ws, nil)
	if err != nil {
		t.Fatalf("LabelsForThreads(nil): %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("empty batch returned %d entries", len(empty))
	}
}

// Deleting a label unfiles its threads (join cascade) and leaves them intact.
func TestDeletingALabelCascadesToItsAssignmentsAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	th := recordInbound(t, ctx, f, "<label-cascade@s.test>", time.Now().UTC())
	label, err := f.store.CreateLabel(ctx, f.ws, "Invoices", "#3b82f6")
	if err != nil {
		t.Fatalf("CreateLabel: %v", err)
	}
	if err := f.store.AssignLabel(ctx, f.ws, th.ID, label.ID); err != nil {
		t.Fatalf("AssignLabel: %v", err)
	}

	if err := f.store.DeleteLabel(ctx, f.ws, label.ID); err != nil {
		t.Fatalf("DeleteLabel: %v", err)
	}

	var rows int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM inbox_thread_labels WHERE label_id = $1`, label.ID).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 0 {
		t.Errorf("%d orphaned assignments after deleting the label, want 0", rows)
	}
	// The THREAD survives: unfiling is not deleting.
	if _, err := f.store.GetThread(ctx, f.ws, th.ID); err != nil {
		t.Errorf("the thread did not survive its label's deletion: %v", err)
	}
}

// ...and the other direction: deleting a thread takes its assignments with it.
func TestDeletingAThreadCascadesToItsLabelsAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	th := recordInbound(t, ctx, f, "<label-thread-cascade@s.test>", time.Now().UTC())
	label, err := f.store.CreateLabel(ctx, f.ws, "Invoices", "#3b82f6")
	if err != nil {
		t.Fatalf("CreateLabel: %v", err)
	}
	if err := f.store.AssignLabel(ctx, f.ws, th.ID, label.ID); err != nil {
		t.Fatalf("AssignLabel: %v", err)
	}

	if _, err := f.pool.Exec(ctx, `DELETE FROM inbox_threads WHERE id = $1`, th.ID); err != nil {
		t.Fatalf("delete thread: %v", err)
	}

	var rows int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM inbox_thread_labels WHERE thread_id = $1`, th.ID).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 0 {
		t.Errorf("%d orphaned assignments after deleting the thread, want 0", rows)
	}
	// The LABEL survives: it is workspace-level, not thread-level.
	if _, err := f.store.GetLabel(ctx, f.ws, label.ID); err != nil {
		t.Errorf("the label did not survive its thread's deletion: %v", err)
	}
}

func TestListThreadsLabelFilterAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	now := time.Now().UTC()
	filed := recordInbound(t, ctx, f, "<label-filter-a@s.test>", now)
	recordInbound(t, ctx, f, "<label-filter-b@s.test>", now.Add(-time.Minute))

	label, err := f.store.CreateLabel(ctx, f.ws, "Invoices", "#3b82f6")
	if err != nil {
		t.Fatalf("CreateLabel: %v", err)
	}
	if err := f.store.AssignLabel(ctx, f.ws, filed.ID, label.ID); err != nil {
		t.Fatalf("AssignLabel: %v", err)
	}

	page, err := f.store.ListThreads(ctx, f.ws, inbox.ListFilter{LabelID: &label.ID})
	if err != nil {
		t.Fatalf("ListThreads(label): %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != filed.ID {
		t.Errorf("label-filtered page = %+v, want only %s", page.Items, filed.ID)
	}

	// An unknown label id matches nothing rather than erroring.
	unknown := uuid.New()
	none, err := f.store.ListThreads(ctx, f.ws, inbox.ListFilter{LabelID: &unknown})
	if err != nil {
		t.Fatalf("ListThreads(unknown label): %v", err)
	}
	if len(none.Items) != 0 {
		t.Errorf("an unknown label matched %d threads, want 0", len(none.Items))
	}
}

// The rail's per-label counts exclude snoozed threads, for the same reason
// every other counter does: they are absent from the list the count labels.
func TestCountThreadsByLabelExcludesSnoozedAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	now := time.Now().UTC()
	visible := recordInbound(t, ctx, f, "<label-count-a@s.test>", now)
	snoozed := recordInbound(t, ctx, f, "<label-count-b@s.test>", now.Add(-time.Minute))

	label, err := f.store.CreateLabel(ctx, f.ws, "Invoices", "#3b82f6")
	if err != nil {
		t.Fatalf("CreateLabel: %v", err)
	}
	for _, id := range []uuid.UUID{visible.ID, snoozed.ID} {
		if err := f.store.AssignLabel(ctx, f.ws, id, label.ID); err != nil {
			t.Fatalf("AssignLabel: %v", err)
		}
	}

	before, err := f.store.CountThreadsByLabel(ctx, f.ws)
	if err != nil {
		t.Fatalf("CountThreadsByLabel: %v", err)
	}
	if len(before) != 1 || before[0].Total != 2 {
		t.Fatalf("before = %+v, want one entry totalling 2", before)
	}

	if _, err := f.store.UpsertSnooze(ctx, inbox.UpsertSnoozeInput{
		WorkspaceID: f.ws, ThreadID: snoozed.ID, SnoozeUntil: now.Add(48 * time.Hour),
	}); err != nil {
		t.Fatalf("UpsertSnooze: %v", err)
	}

	after, err := f.store.CountThreadsByLabel(ctx, f.ws)
	if err != nil {
		t.Fatalf("CountThreadsByLabel(after): %v", err)
	}
	if len(after) != 1 || after[0].Total != 1 {
		t.Errorf("after = %+v, want one entry totalling 1", after)
	}
}

// A name at the length cap must round-trip through Postgres unchanged — TEXT
// has no length limit, so the cap is ours and must not be silently truncated.
func TestLabelNameAtTheCapRoundTripsAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	name := strings.Repeat("é", inbox.MaxLabelNameLength)

	created, err := f.store.CreateLabel(ctx, f.ws, name, "#3b82f6")
	if err != nil {
		t.Fatalf("CreateLabel: %v", err)
	}
	if created.Name != name {
		t.Errorf("stored name has %d runes, want %d", len([]rune(created.Name)), len([]rune(name)))
	}
}
