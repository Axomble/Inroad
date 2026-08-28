//go:build integration

package deadletter

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/dbtest"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// These tests exercise PgStore against real Postgres. The unit tests use a fake
// Store whose ClaimReplay reproduces the status guard under a mutex; that proves
// the SERVICE calls it correctly, but it cannot prove the SQL predicate actually
// serialises — which is the assumption the whole double-send guarantee rests on.
// This file closes that gap on the real statement. Docker must be up.

func setup(t *testing.T) (*pgxpool.Pool, *PgStore) {
	t.Helper()
	ctx := context.Background()
	if err := db.Migrate(dbtest.DSN(t)); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := db.Connect(ctx, dbtest.DSN(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool, NewPgStore(gen.New(pool))
}

// mintWorkspace creates a real workspace row, because task_dead_letters.
// workspace_id is a NOT NULL foreign key — an invented UUID would fail the
// insert, which is itself part of what these tests confirm.
func mintWorkspace(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	var ws uuid.UUID
	if err := pool.QueryRow(context.Background(),
		"INSERT INTO workspaces (name) VALUES ('deadletter-it') RETURNING id").Scan(&ws); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	return ws
}

func seedRow(t *testing.T, store *PgStore, ws uuid.UUID) gen.TaskDeadLetter {
	t.Helper()
	payload, err := json.Marshal(map[string]string{
		"enrollment_id": uuid.NewString(),
		"workspace_id":  ws.String(),
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	row, err := store.Insert(context.Background(), Capture{
		WorkspaceID: ws, TaskType: "sequence:advance", Payload: payload,
		LastError: "dial timeout", AttemptCount: 5,
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	return row
}

// A captured row lands as 'pending' with no replayed_at, and the JSONB payload
// round-trips byte-equivalently — replay re-enqueues exactly what was captured.
func TestInsertRoundTrip(t *testing.T) {
	pool, store := setup(t)
	ws := mintWorkspace(t, pool)
	row := seedRow(t, store, ws)

	if row.Status != StatusPending {
		t.Errorf("status = %q, want %q", row.Status, StatusPending)
	}
	if row.ReplayedAt.Valid {
		t.Error("a fresh capture already has replayed_at set")
	}
	if row.AttemptCount != 5 {
		t.Errorf("attempt_count = %d, want 5", row.AttemptCount)
	}

	got, ok, err := store.Get(context.Background(), ws, row.ID)
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	var original, reread map[string]string
	if err := json.Unmarshal(row.Payload, &original); err != nil {
		t.Fatalf("unmarshal original: %v", err)
	}
	if err := json.Unmarshal(got.Payload, &reread); err != nil {
		t.Fatalf("unmarshal reread: %v", err)
	}
	if original["enrollment_id"] != reread["enrollment_id"] {
		t.Error("payload did not survive the JSONB round trip")
	}
}

// THE test this file exists for: N goroutines claiming the same row against real
// Postgres must produce exactly one winner. If the status='pending' predicate
// did not serialise, this is where it would show.
func TestClaimReplayIsAtomicUnderRealConcurrency(t *testing.T) {
	pool, store := setup(t)
	ws := mintWorkspace(t, pool)
	row := seedRow(t, store, ws)

	const racers = 8
	var wg sync.WaitGroup
	claimed := make([]bool, racers)
	errs := make([]error, racers)
	start := make(chan struct{})
	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, claimed[i], errs[i] = store.ClaimReplay(context.Background(), ws, row.ID)
		}()
	}
	close(start)
	wg.Wait()

	winners := 0
	for i := range racers {
		if errs[i] != nil {
			t.Errorf("racer %d: %v", i, errs[i])
		}
		if claimed[i] {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("%d racers won the claim, want exactly 1 — the SQL guard does not serialise", winners)
	}

	got, _, err := store.Get(context.Background(), ws, row.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != StatusReplayed {
		t.Errorf("status = %q, want %q", got.Status, StatusReplayed)
	}
	if !got.ReplayedAt.Valid {
		t.Error("replayed_at was not set by the winning claim")
	}
}

// Tenant isolation at the SQL level: another workspace's id must match nothing
// on any statement, so no WHERE clause can be relied on by accident.
func TestEveryStatementIsWorkspacePinned(t *testing.T) {
	pool, store := setup(t)
	ws := mintWorkspace(t, pool)
	intruder := mintWorkspace(t, pool)
	row := seedRow(t, store, ws)
	ctx := context.Background()

	if _, ok, err := store.Get(ctx, intruder, row.ID); err != nil || ok {
		t.Errorf("Get across tenants: ok=%v err=%v, want ok=false", ok, err)
	}
	if _, ok, err := store.ClaimReplay(ctx, intruder, row.ID); err != nil || ok {
		t.Errorf("ClaimReplay across tenants: ok=%v err=%v, want ok=false", ok, err)
	}
	if ok, err := store.Discard(ctx, intruder, row.ID); err != nil || ok {
		t.Errorf("Discard across tenants: ok=%v err=%v, want ok=false", ok, err)
	}
	rows, err := store.List(ctx, intruder, ListQuery{Limit: 100})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, r := range rows {
		if r.ID == row.ID {
			t.Fatal("List leaked another workspace's dead letter")
		}
	}

	// And after all that, the row is untouched.
	got, ok, err := store.Get(ctx, ws, row.ID)
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if got.Status != StatusPending {
		t.Errorf("status = %q, want it untouched at %q", got.Status, StatusPending)
	}
}

// The status filter and its "" any-status sentinel, against the real index path.
func TestListFilterByStatus(t *testing.T) {
	pool, store := setup(t)
	ws := mintWorkspace(t, pool)
	ctx := context.Background()

	pending := seedRow(t, store, ws)
	replayed := seedRow(t, store, ws)
	discarded := seedRow(t, store, ws)
	if _, ok, err := store.ClaimReplay(ctx, ws, replayed.ID); err != nil || !ok {
		t.Fatalf("ClaimReplay: ok=%v err=%v", ok, err)
	}
	if ok, err := store.Discard(ctx, ws, discarded.ID); err != nil || !ok {
		t.Fatalf("Discard: ok=%v err=%v", ok, err)
	}

	cases := map[string]uuid.UUID{
		StatusPending:   pending.ID,
		StatusReplayed:  replayed.ID,
		StatusDiscarded: discarded.ID,
	}
	for status, wantID := range cases {
		rows, err := store.List(ctx, ws, ListQuery{Status: status, Limit: 100})
		if err != nil {
			t.Fatalf("List(%q): %v", status, err)
		}
		if len(rows) != 1 {
			t.Errorf("List(%q) returned %d rows, want 1", status, len(rows))
			continue
		}
		if rows[0].ID != wantID {
			t.Errorf("List(%q) returned the wrong row", status)
		}
	}

	all, err := store.List(ctx, ws, ListQuery{Limit: 100})
	if err != nil {
		t.Fatalf("List(any): %v", err)
	}
	if len(all) != 3 {
		t.Errorf("List(any) returned %d rows, want 3", len(all))
	}
	// Newest first, per the ORDER BY the index serves.
	if all[0].CreatedAt.Time.Before(all[len(all)-1].CreatedAt.Time) {
		t.Error("List is not ordered newest first")
	}
}

// THE REGRESSION THIS ENDPOINT'S PAGING SHAPE EXISTS FOR, against real Postgres.
//
// A dead-letter list is a queue the reader MUTATES: triaging a row (replay or
// discard) does not delete it, but it does move it out of the status=pending set
// the operator is paging through. Under OFFSET that is silent data loss — every
// row triaged on page one shifts the rest up, so OFFSET 3 starts three rows past
// where page two actually begins and those rows are never shown. Under a keyset
// cursor page two resumes at the ROW page one stopped at, so nothing can slip
// between the pages no matter what left the set.
//
// The test triages half of page one before reading page two, which is not a
// contrived race — it is the literal workflow the screen is for.
func TestKeysetPagingSkipsNothingWhenPageOneIsTriaged(t *testing.T) {
	pool, store := setup(t)
	ws := mintWorkspace(t, pool)
	ctx := context.Background()
	const total, pageSize = 6, 3
	for range total {
		seedRow(t, store, ws)
	}

	// Ground truth: the full pending list in the order the index serves it.
	// Derived by reading rather than assumed from insertion order, so a
	// created_at tie between two inserts cannot make the expectation wrong.
	all, err := store.List(ctx, ws, ListQuery{Status: StatusPending, Limit: 100})
	if err != nil {
		t.Fatalf("List(all): %v", err)
	}
	if len(all) != total {
		t.Fatalf("seeded %d rows, read back %d", total, len(all))
	}

	first, err := store.List(ctx, ws, ListQuery{Status: StatusPending, Limit: pageSize})
	if err != nil {
		t.Fatalf("List(page 1): %v", err)
	}
	if len(first) != pageSize {
		t.Fatalf("page 1 has %d rows, want %d", len(first), pageSize)
	}
	for i := range first {
		if first[i].ID != all[i].ID {
			t.Fatalf("page 1 row %d is not the %dth row of the full list", i, i)
		}
	}

	// Triage two of page one's three rows — the whole point of the screen.
	for _, row := range first[:2] {
		if ok, err := store.Discard(ctx, ws, row.ID); err != nil || !ok {
			t.Fatalf("Discard: ok=%v err=%v", ok, err)
		}
	}

	// Page two, resumed from the LAST ROW OF PAGE ONE.
	last := first[len(first)-1]
	second, err := store.List(ctx, ws, ListQuery{
		Status: StatusPending,
		Cursor: &Cursor{CreatedAt: last.CreatedAt.Time, ID: last.ID},
		Limit:  pageSize,
	})
	if err != nil {
		t.Fatalf("List(page 2): %v", err)
	}

	// Every row after the cursor, still pending, exactly once and in order.
	want := all[pageSize:]
	if len(second) != len(want) {
		t.Fatalf("page 2 has %d rows, want %d — rows were skipped or repeated", len(second), len(want))
	}
	for i := range want {
		if second[i].ID != want[i].ID {
			t.Errorf("page 2 row %d = %v, want %v", i, second[i].ID, want[i].ID)
		}
	}
	// The cursor row itself must NOT reappear: the seek is strictly-after.
	for _, row := range second {
		if row.ID == last.ID {
			t.Error("page 2 repeated the row page 1 ended on; the seek is not strict")
		}
	}

	// And the premise, stated as an assertion rather than a comment: the OFFSET
	// form this replaced does skip here. If this ever stops being true the
	// migration away from OFFSET has lost its justification and should be
	// re-examined, not silently kept.
	var offsetPageTwo []uuid.UUID
	rows, err := pool.Query(ctx,
		`SELECT id FROM task_dead_letters
		 WHERE workspace_id = $1 AND status = 'pending'
		 ORDER BY created_at DESC, id DESC
		 LIMIT $2 OFFSET $2`, ws, pageSize)
	if err != nil {
		t.Fatalf("offset query: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		offsetPageTwo = append(offsetPageTwo, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("offset query rows: %v", err)
	}
	if len(offsetPageTwo) >= len(second) {
		t.Errorf("OFFSET page 2 returned %d rows and keyset returned %d — OFFSET was expected "+
			"to lose the rows that shifted up when page 1 was triaged",
			len(offsetPageTwo), len(second))
	}
}

// A cursor past the end of the list is an empty page, not an error.
func TestListPastTheEndIsEmpty(t *testing.T) {
	pool, store := setup(t)
	ws := mintWorkspace(t, pool)
	ctx := context.Background()
	seedRow(t, store, ws)

	rows, err := store.List(ctx, ws, ListQuery{Limit: 10})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("read back %d rows, want 1", len(rows))
	}
	beyond, err := store.List(ctx, ws, ListQuery{
		Cursor: &Cursor{CreatedAt: rows[0].CreatedAt.Time, ID: rows[0].ID},
		Limit:  10,
	})
	if err != nil {
		t.Fatalf("List past the end: %v", err)
	}
	if len(beyond) != 0 {
		t.Errorf("page past the end has %d rows, want 0", len(beyond))
	}
}

// The cursor is workspace-pinned by the STATEMENT, not by the token: a cursor is
// caller-supplied, so the seek predicate must never be able to widen the tenant
// filter. Replaying a valid cursor as another tenant returns nothing.
func TestACursorCannotCrossTenants(t *testing.T) {
	pool, store := setup(t)
	ws := mintWorkspace(t, pool)
	intruder := mintWorkspace(t, pool)
	ctx := context.Background()
	for range 3 {
		seedRow(t, store, ws)
	}

	mine, err := store.List(ctx, ws, ListQuery{Limit: 1})
	if err != nil || len(mine) != 1 {
		t.Fatalf("List: rows=%d err=%v", len(mine), err)
	}
	cur := &Cursor{CreatedAt: mine[0].CreatedAt.Time, ID: mine[0].ID}

	stolen, err := store.List(ctx, intruder, ListQuery{Cursor: cur, Limit: 10})
	if err != nil {
		t.Fatalf("List as intruder: %v", err)
	}
	if len(stolen) != 0 {
		t.Fatalf("a cursor minted in one workspace returned %d rows in another", len(stolen))
	}
}

// Release restores a claim, and only the EXACT claim it was given: a stale
// replayed_at must not reopen a later, genuine replay.
func TestReleaseReplayIsPinnedToItsOwnClaim(t *testing.T) {
	pool, store := setup(t)
	ws := mintWorkspace(t, pool)
	row := seedRow(t, store, ws)
	ctx := context.Background()

	claimed, ok, err := store.ClaimReplay(ctx, ws, row.ID)
	if err != nil || !ok {
		t.Fatalf("ClaimReplay: ok=%v err=%v", ok, err)
	}

	// A release naming a DIFFERENT claim instant must not fire.
	stale := claimed.ReplayedAt
	stale.Time = stale.Time.Add(-1)
	if err := store.ReleaseReplay(ctx, ws, row.ID, stale); err != nil {
		t.Fatalf("ReleaseReplay(stale): %v", err)
	}
	got, _, err := store.Get(ctx, ws, row.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != StatusReplayed {
		t.Fatalf("a stale release reopened the row: status = %q", got.Status)
	}

	// The real one does.
	if err := store.ReleaseReplay(ctx, ws, row.ID, claimed.ReplayedAt); err != nil {
		t.Fatalf("ReleaseReplay: %v", err)
	}
	got, _, err = store.Get(ctx, ws, row.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != StatusPending {
		t.Errorf("status = %q, want %q", got.Status, StatusPending)
	}
	if got.ReplayedAt.Valid {
		t.Error("replayed_at survived the release")
	}
}

// Discard is status-guarded the same way the claim is: an already-replayed row
// cannot be rewritten as discarded.
func TestDiscardIsStatusGuarded(t *testing.T) {
	pool, store := setup(t)
	ws := mintWorkspace(t, pool)
	row := seedRow(t, store, ws)
	ctx := context.Background()

	if _, ok, err := store.ClaimReplay(ctx, ws, row.ID); err != nil || !ok {
		t.Fatalf("ClaimReplay: ok=%v err=%v", ok, err)
	}
	if ok, err := store.Discard(ctx, ws, row.ID); err != nil || ok {
		t.Errorf("Discard of a replayed row: ok=%v err=%v, want ok=false", ok, err)
	}
}

// The workspace FK is real: a dead letter cannot be orphaned onto a workspace
// that does not exist, which is what keeps the tenant-scoped list total.
func TestInsertRejectsAnUnknownWorkspace(t *testing.T) {
	_, store := setup(t)
	_, err := store.Insert(context.Background(), Capture{
		WorkspaceID: uuid.New(), TaskType: "sequence:advance", Payload: []byte(`{}`),
	})
	if err == nil {
		t.Fatal("insert succeeded for a workspace that does not exist")
	}
}
