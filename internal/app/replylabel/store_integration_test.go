//go:build integration

package replylabel_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/app/replylabel"
	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/dbtest"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// These tests exercise migration 000047 against Postgres: the per-workspace
// seed, the AFTER INSERT ON workspaces trigger that keeps new workspaces in
// step with it, and the SQL-level builtin delete guard. Docker must be up.

func connect(t *testing.T) (*pgxpool.Pool, *gen.Queries) {
	t.Helper()
	if err := db.Migrate(dbtest.DSN(t)); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := db.Connect(context.Background(), dbtest.DSN(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool, gen.New(pool)
}

// wantSeeded is the automation contract migration 000047 promises: these seven
// keys with these flags reproduce the pre-taxonomy poll.go switch exactly. The
// worker's dispatch tests assert the BEHAVIOUR; this asserts the DATA the
// migration must produce for that behaviour to hold in a real workspace.
var wantSeeded = map[string]struct {
	stops, automated, suppresses, captures, defers bool
}{
	"positive":      {stops: true, captures: true},
	"negative":      {stops: true},
	"neutral":       {stops: true},
	"unsubscribe":   {stops: true, suppresses: true},
	"out_of_office": {automated: true},
	"auto_reply":    {automated: true},
	"unknown":       {stops: true},
}

// TestNewWorkspaceIsSeededByTrigger: a workspace created AFTER the migration
// gets the same seven labels the backfill produced, because both go through
// seed_reply_labels(). No drift is possible by construction.
func TestNewWorkspaceIsSeededByTrigger(t *testing.T) {
	ctx := context.Background()
	pool, q := connect(t)
	ws, err := q.CreateWorkspace(ctx, "ReplyLabel IT "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	svc := replylabel.NewService(replylabel.NewPgStore(pool))
	labels, err := svc.List(ctx, ws.ID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(labels) != len(wantSeeded) {
		t.Fatalf("seeded %d labels, want %d: %+v", len(labels), len(wantSeeded), labels)
	}
	for _, l := range labels {
		want, known := wantSeeded[l.Key]
		if !known {
			t.Fatalf("unexpected seeded key %q", l.Key)
		}
		if !l.IsBuiltin {
			t.Errorf("%s: seeded labels must be builtin", l.Key)
		}
		if l.StopsEnrollment != want.stops || l.IsAutomated != want.automated ||
			l.SuppressesContact != want.suppresses || l.CapturesDeal != want.captures ||
			l.DefersEnrollment != want.defers {
			t.Errorf("%s: flags = stop%v auto%v supp%v cap%v defer%v, want %+v",
				l.Key, l.StopsEnrollment, l.IsAutomated, l.SuppressesContact,
				l.CapturesDeal, l.DefersEnrollment, want)
		}
	}
}

// TestBuiltinDeleteIsGuardedInSQLToo: the service refuses first, but the DELETE
// is ALSO guarded on NOT is_builtin, so neither guard alone is load-bearing.
func TestBuiltinDeleteIsGuardedInSQLToo(t *testing.T) {
	ctx := context.Background()
	pool, q := connect(t)
	ws, err := q.CreateWorkspace(ctx, "ReplyLabel IT guard "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	store := replylabel.NewPgStore(pool)
	labels, err := store.List(ctx, ws.ID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	deleted, err := store.Delete(ctx, ws.ID, labels[0].ID)
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if deleted {
		t.Fatalf("the store deleted builtin %q", labels[0].Key)
	}
}

// TestCustomLabelRoundTrips covers create → resolve → reorder → delete against
// the real schema (key uniqueness, the position default, and the reorder
// transaction).
func TestCustomLabelRoundTrips(t *testing.T) {
	ctx := context.Background()
	pool, q := connect(t)
	ws, err := q.CreateWorkspace(ctx, "ReplyLabel IT custom "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	svc := replylabel.NewService(replylabel.NewPgStore(pool))

	created, err := svc.Create(ctx, ws.ID, replylabel.Input{
		Label: "Demo Requested", Color: "#22C55E", StopsEnrollment: true, CapturesDeal: true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Key != "demo_requested" || created.IsBuiltin {
		t.Fatalf("unexpected created label: %+v", created)
	}

	// A second label whose name slugifies to the same key is a conflict, not a
	// duplicate row (UNIQUE (workspace_id, key)).
	if _, err := svc.Create(ctx, ws.ID, replylabel.Input{Label: "demo requested", Color: "#22C55E"}); err == nil {
		t.Fatal("expected a duplicate-key conflict")
	}

	resolved, ok, err := svc.Resolve(ctx, ws.ID, "demo_requested")
	if err != nil || !ok {
		t.Fatalf("Resolve: ok=%v err=%v", ok, err)
	}
	if !resolved.CapturesDeal {
		t.Fatal("captures_deal did not persist")
	}

	// Reverse the whole list and read it back in the new order.
	all, err := svc.List(ctx, ws.ID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	ids := make([]uuid.UUID, len(all))
	for i := range all {
		ids[i] = all[len(all)-1-i].ID
	}
	reordered, err := svc.Reorder(ctx, ws.ID, ids)
	if err != nil {
		t.Fatalf("Reorder: %v", err)
	}
	if reordered[0].ID != ids[0] {
		t.Fatalf("reorder did not take: first = %s, want %s", reordered[0].ID, ids[0])
	}

	if err := svc.Delete(ctx, ws.ID, created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok, err := svc.Resolve(ctx, ws.ID, "demo_requested"); err != nil || ok {
		t.Fatalf("the deleted key must no longer resolve: ok=%v err=%v", ok, err)
	}
}

// TestEnrollmentAcceptsACustomReplyClass: migration 000047 drops the CHECK that
// pinned reply_class to the seven builtin classes. Without that drop a custom
// label could be created but never recorded.
func TestEnrollmentAcceptsACustomReplyClass(t *testing.T) {
	ctx := context.Background()
	pool, q := connect(t)
	ws, err := q.CreateWorkspace(ctx, "ReplyLabel IT class "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	mb, err := q.CreateMailbox(ctx, gen.CreateMailboxParams{
		WorkspaceID: ws.ID, Provider: "smtp", Email: "from@acme.test", DisplayName: "Acme",
		SmtpHost: "smtp.acme.test", SmtpPort: 587, SmtpUsername: "from@acme.test",
		ImapHost: "imap.acme.test", ImapPort: 993, ImapUsername: "from@acme.test",
		SecretCiphertext: "x", DailyCap: 100, MinIntervalSeconds: 0,
		RampEnabled: false, RampStartCap: 5, RampDays: 30,
	})
	if err != nil {
		t.Fatalf("mailbox: %v", err)
	}
	lst, err := q.CreateList(ctx, gen.CreateListParams{WorkspaceID: ws.ID, Name: "L"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	contact, err := q.UpsertContact(ctx, gen.UpsertContactParams{
		WorkspaceID: ws.ID, Email: "c-" + uuid.NewString() + "@x.test", FirstName: "C",
	})
	if err != nil {
		t.Fatalf("contact: %v", err)
	}
	cam, err := q.CreateCampaign(ctx, gen.CreateCampaignParams{
		WorkspaceID: ws.ID, Name: "Camp", MailboxID: mb.ID, ListID: lst.ID,
		Subject: "Hi", BodyText: "Hello",
	})
	if err != nil {
		t.Fatalf("campaign: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO sequence_enrollments (workspace_id, campaign_id, contact_id, next_due_at, reply_class)
		 VALUES ($1,$2,$3, now(), 'demo_requested')`, ws.ID, cam.ID, contact.ID); err != nil {
		t.Fatalf("a custom reply_class must be storable: %v", err)
	}
}
