//go:build integration

package campaign

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// seedCampaign creates one workspace + mailbox + list + campaign (via the raw
// sqlc queries, mirroring the tracking package's integration seed) and returns
// the workspace, list, and campaign ids. It does NOT enroll anyone -- callers
// add contacts to the returned list, then EnrollListMembers.
func seedCampaign(t *testing.T, ctx context.Context, q *gen.Queries, label string) (ws, listID, campaignID uuid.UUID) {
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
		Subject: "Hi", BodyText: "hello",
	})
	if err != nil {
		t.Fatalf("campaign: %v", err)
	}
	return w.ID, lst.ID, cam.ID
}

// addContact upserts a contact into the workspace and adds it to the list, so
// EnrollListMembers will materialize an enrollment for it.
func addContact(t *testing.T, ctx context.Context, q *gen.Queries, ws, listID uuid.UUID, email, first string) {
	t.Helper()
	ct, err := q.UpsertContact(ctx, gen.UpsertContactParams{WorkspaceID: ws, Email: email, FirstName: first})
	if err != nil {
		t.Fatalf("contact %s: %v", email, err)
	}
	if err := q.AddListMember(ctx, gen.AddListMemberParams{ListID: listID, ContactID: ct.ID}); err != nil {
		t.Fatalf("member %s: %v", email, err)
	}
}

// setReply stamps an explicit replied_at (+ class/source/confidence) on the
// enrollment for the given contact email. Using a direct UPDATE with an
// explicit timestamp -- rather than SetEnrollmentReplyClass, which stamps
// replied_at = now() -- makes the ORDER BY replied_at ordering deterministic.
func setReply(t *testing.T, ctx context.Context, pool *pgxpool.Pool, ws uuid.UUID, email string, repliedAt time.Time, class, source string, confidence float32) {
	t.Helper()
	tag, err := pool.Exec(ctx, `
		UPDATE sequence_enrollments e
		SET replied_at = $3, reply_class = $4, reply_source = $5, reply_confidence = $6
		FROM contacts c
		WHERE e.contact_id = c.id AND c.email = $1 AND e.workspace_id = $2`,
		email, ws, repliedAt, class, source, confidence)
	if err != nil {
		t.Fatalf("set reply %s: %v", email, err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("set reply %s: affected %d rows, want 1", email, tag.RowsAffected())
	}
}

func emails(rows []gen.ListCampaignEnrollmentsRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Email
	}
	return out
}

// TestListCampaignEnrollments exercises the ListCampaignEnrollments query
// against real Postgres: the join to contacts, the
// "replied_at DESC NULLS LAST, email ASC" ordering, the workspace_id pin, and
// LIMIT/OFFSET pagination. Unit tests cover the service against a fake store;
// this closes the live-DB gap on the SQL itself.
func TestListCampaignEnrollments(t *testing.T) {
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

	// One workspace, one campaign, four enrolled contacts.
	ws, listID, campaignID := seedCampaign(t, ctx, q, "Enroll WS1")
	addContact(t, ctx, q, ws, listID, "alice@x.test", "Alice")
	addContact(t, ctx, q, ws, listID, "bob@x.test", "Bob")
	addContact(t, ctx, q, ws, listID, "carol@x.test", "Carol")
	addContact(t, ctx, q, ws, listID, "dave@x.test", "Dave")

	if _, err := q.EnrollListMembers(ctx, gen.EnrollListMembersParams{ID: campaignID, WorkspaceID: ws}); err != nil {
		t.Fatalf("enroll: %v", err)
	}

	// bob replied more recently than alice; carol and dave never replied.
	base := time.Now().UTC().Truncate(time.Microsecond)
	aliceRepliedAt := base.Add(-2 * time.Hour)
	bobRepliedAt := base.Add(-1 * time.Hour)
	setReply(t, ctx, pool, ws, "alice@x.test", aliceRepliedAt, "negative", "lexicon", 0.7)
	setReply(t, ctx, pool, ws, "bob@x.test", bobRepliedAt, "positive", "model", 0.95)

	// Most-recent reply first, then never-replied rows ordered by email ASC.
	wantOrder := []string{"bob@x.test", "alice@x.test", "carol@x.test", "dave@x.test"}

	t.Run("ordering and join", func(t *testing.T) {
		rows, err := store.ListEnrollments(ctx, ws, campaignID, 100, 0)
		if err != nil {
			t.Fatalf("ListEnrollments: %v", err)
		}
		if got := emails(rows); !equalStrings(got, wantOrder) {
			t.Fatalf("order: got %v want %v", got, wantOrder)
		}

		byEmail := make(map[string]gen.ListCampaignEnrollmentsRow, len(rows))
		for _, r := range rows {
			byEmail[r.Email] = r
		}

		// bob: replied row, fields joined from contacts + enrollment.
		bob := byEmail["bob@x.test"]
		if bob.FirstName != "Bob" {
			t.Errorf("bob first_name: got %q want Bob", bob.FirstName)
		}
		if bob.Status != "active" {
			t.Errorf("bob status: got %q want active", bob.Status)
		}
		if bob.ReplyClass == nil || *bob.ReplyClass != "positive" {
			t.Errorf("bob reply_class: got %v want positive", bob.ReplyClass)
		}
		if bob.ReplySource == nil || *bob.ReplySource != "model" {
			t.Errorf("bob reply_source: got %v want model", bob.ReplySource)
		}
		if !bob.RepliedAt.Valid || !bob.RepliedAt.Time.Equal(bobRepliedAt) {
			t.Errorf("bob replied_at: got %+v want %v", bob.RepliedAt, bobRepliedAt)
		}

		// alice: the older reply.
		alice := byEmail["alice@x.test"]
		if alice.ReplyClass == nil || *alice.ReplyClass != "negative" {
			t.Errorf("alice reply_class: got %v want negative", alice.ReplyClass)
		}
		if alice.ReplySource == nil || *alice.ReplySource != "lexicon" {
			t.Errorf("alice reply_source: got %v want lexicon", alice.ReplySource)
		}
		if !alice.RepliedAt.Valid || !alice.RepliedAt.Time.Equal(aliceRepliedAt) {
			t.Errorf("alice replied_at: got %+v want %v", alice.RepliedAt, aliceRepliedAt)
		}

		// carol: never replied -> null class/source/replied_at.
		carol := byEmail["carol@x.test"]
		if carol.FirstName != "Carol" {
			t.Errorf("carol first_name: got %q want Carol", carol.FirstName)
		}
		if carol.ReplyClass != nil {
			t.Errorf("carol reply_class: got %v want nil", *carol.ReplyClass)
		}
		if carol.ReplySource != nil {
			t.Errorf("carol reply_source: got %v want nil", *carol.ReplySource)
		}
		if carol.RepliedAt.Valid {
			t.Errorf("carol replied_at: got %v want NULL", carol.RepliedAt.Time)
		}
	})

	t.Run("workspace pin ignores cross-tenant campaign id", func(t *testing.T) {
		// A second, unrelated workspace + campaign. Asking for ws1's real
		// campaign id under ws2's workspace id must return zero rows -- the
		// SQL "AND workspace_id = $2" filters it even though the campaign id
		// exists, so the service's ownership check is defended in depth.
		ws2, _, _ := seedCampaign(t, ctx, q, "Enroll WS2")
		rows, err := store.ListEnrollments(ctx, ws2, campaignID, 100, 0)
		if err != nil {
			t.Fatalf("cross-tenant ListEnrollments: %v", err)
		}
		if len(rows) != 0 {
			t.Fatalf("cross-tenant leaked %d rows: %v", len(rows), emails(rows))
		}
	})

	t.Run("pagination windows are disjoint and ordered", func(t *testing.T) {
		page1, err := store.ListEnrollments(ctx, ws, campaignID, 2, 0)
		if err != nil {
			t.Fatalf("page1: %v", err)
		}
		if got, want := emails(page1), wantOrder[:2]; !equalStrings(got, want) {
			t.Fatalf("page1: got %v want %v", got, want)
		}
		page2, err := store.ListEnrollments(ctx, ws, campaignID, 2, 2)
		if err != nil {
			t.Fatalf("page2: %v", err)
		}
		if got, want := emails(page2), wantOrder[2:]; !equalStrings(got, want) {
			t.Fatalf("page2: got %v want %v", got, want)
		}
	})
}

func equalStrings(a, b []string) bool {
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
