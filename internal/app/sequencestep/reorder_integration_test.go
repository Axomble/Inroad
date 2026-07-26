//go:build integration

package sequencestep

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

func dsn() string {
	if v := os.Getenv("INROAD_DATABASE_URL"); v != "" {
		return v
	}
	return "postgres://inroad:inroad@localhost:5433/inroad?sslmode=disable"
}

// seedThreeSteps creates a workspace + mailbox + list + draft campaign and three
// steps (orders 1,2,3), returning the workspace, campaign id, and step ids in
// their initial order.
func seedThreeSteps(t *testing.T, ctx context.Context, q *gen.Queries, label string) (ws, campaignID uuid.UUID, ids []uuid.UUID) {
	t.Helper()
	w, err := q.CreateWorkspace(ctx, label+" "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	mb, err := q.CreateMailbox(ctx, gen.CreateMailboxParams{
		WorkspaceID: w.ID, Provider: "smtp", Email: "from@x.test", DisplayName: "X",
		SmtpHost: "smtp.x", SmtpPort: 587, SmtpUsername: "from@x.test",
		ImapHost: "imap.x", ImapPort: 993, ImapUsername: "from@x.test",
		SecretCiphertext: "ct", DailyCap: 50,
		MinIntervalSeconds: 0, RampEnabled: false, RampStartCap: 5, RampDays: 30,
	})
	if err != nil {
		t.Fatalf("mailbox: %v", err)
	}
	lst, err := q.CreateList(ctx, gen.CreateListParams{WorkspaceID: w.ID, Name: "L"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	cam, err := q.CreateCampaign(ctx, gen.CreateCampaignParams{
		WorkspaceID: w.ID, Name: "Camp", MailboxID: mb.ID, ListID: lst.ID,
		Subject: "s1", BodyText: "b1",
	})
	if err != nil {
		t.Fatalf("campaign: %v", err)
	}
	for i := int32(1); i <= 3; i++ {
		st, err := q.CreateStep(ctx, gen.CreateStepParams{
			WorkspaceID: w.ID, CampaignID: cam.ID, StepOrder: i, DelaySeconds: 0,
			Subject: "s", BodyText: "b",
		})
		if err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		ids = append(ids, st.ID)
	}
	return w.ID, cam.ID, ids
}

func orderedIDs(steps []gen.SequenceStep) []uuid.UUID {
	out := make([]uuid.UUID, len(steps))
	for i, st := range steps {
		out[i] = st.ID
	}
	return out
}

// TestReorderPersistsNewOrder exercises PgStore.Reorder against real Postgres:
// the two-phase shift-then-stamp must rewrite step_order to 1..N in the target
// order without tripping the UNIQUE(campaign_id, step_order) constraint, and the
// returned + persisted rows must reflect the new order.
func TestReorderPersistsNewOrder(t *testing.T) {
	ctx := context.Background()
	if err := db.Migrate(dsn()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := db.Connect(ctx, dsn())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	q := gen.New(pool)
	store := NewPgStore(pool)

	ws, campaignID, ids := seedThreeSteps(t, ctx, q, "Reorder WS1")
	// Reverse the order: [3,2,1].
	want := []uuid.UUID{ids[2], ids[1], ids[0]}

	got, err := store.Reorder(ctx, ws, campaignID, want)
	if err != nil {
		t.Fatalf("Reorder: %v", err)
	}
	if gotIDs := orderedIDs(got); !equalIDs(gotIDs, want) {
		t.Fatalf("returned order: got %v want %v", gotIDs, want)
	}
	// step_order must be a clean 1..N.
	for i, st := range got {
		if st.StepOrder != int32(i+1) {
			t.Fatalf("step %d order: got %d want %d", i, st.StepOrder, i+1)
		}
	}
	// Persisted (re-read) order matches.
	after, err := q.ListStepsByCampaign(ctx, gen.ListStepsByCampaignParams{CampaignID: campaignID, WorkspaceID: ws})
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if gotIDs := orderedIDs(after); !equalIDs(gotIDs, want) {
		t.Fatalf("persisted order: got %v want %v", gotIDs, want)
	}
}

// TestReorderIsWorkspaceScoped proves the workspace pin: reordering under a
// foreign workspace id touches zero rows (returns an empty list) and leaves the
// owning workspace's step_order untouched.
func TestReorderIsWorkspaceScoped(t *testing.T) {
	ctx := context.Background()
	if err := db.Migrate(dsn()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := db.Connect(ctx, dsn())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	q := gen.New(pool)
	store := NewPgStore(pool)

	ws, campaignID, ids := seedThreeSteps(t, ctx, q, "Reorder WS-owner")
	other, err := q.CreateWorkspace(ctx, "Reorder WS-intruder "+uuid.NewString())
	if err != nil {
		t.Fatalf("intruder workspace: %v", err)
	}

	// Intruder attempts to reorder the owner's steps under its own workspace id.
	got, err := store.Reorder(ctx, other.ID, campaignID, []uuid.UUID{ids[2], ids[1], ids[0]})
	if err != nil {
		t.Fatalf("cross-tenant Reorder: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("cross-tenant reorder returned %d rows, want 0", len(got))
	}
	// Owner's order is unchanged (still 1,2,3 in the original id order).
	after, err := q.ListStepsByCampaign(ctx, gen.ListStepsByCampaignParams{CampaignID: campaignID, WorkspaceID: ws})
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if gotIDs := orderedIDs(after); !equalIDs(gotIDs, ids) {
		t.Fatalf("owner order mutated by cross-tenant reorder: got %v want %v", gotIDs, ids)
	}
}

func equalIDs(a, b []uuid.UUID) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
