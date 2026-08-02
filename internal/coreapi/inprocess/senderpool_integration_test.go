//go:build integration

package inprocess

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/crypto"
	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/dbtest"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/rotation"
	"github.com/inroad/inroad/internal/platform/warmup"
)

// These tests exercise sender-pool assignment against Postgres: the write-once
// claim, cap-driven routing, the no-pool fallback, and thread pinning across
// follow-up steps. Docker must be up.

type poolFixture struct {
	core       coreapi.Client
	pool       *pgxpool.Pool
	q          *gen.Queries
	ws         uuid.UUID
	foreignWS  uuid.UUID
	campaignID uuid.UUID
	mailboxA   uuid.UUID // also campaigns.mailbox_id (the fallback)
	mailboxB   uuid.UUID
}

// setupPool seeds a workspace with two active mailboxes, a two-step campaign
// (mailbox A is campaigns.mailbox_id) and no sender-pool rows. Tests add pool rows
// as each case needs.
func setupPool(t *testing.T) (context.Context, poolFixture) {
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
	q := gen.New(pool)
	sealer, err := crypto.NewSealer(itMasterKey)
	if err != nil {
		t.Fatalf("sealer: %v", err)
	}
	ct, err := sealer.Seal([]byte("smtp-app-password"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	ws, err := q.CreateWorkspace(ctx, "Pool IT "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	foreign, err := q.CreateWorkspace(ctx, "Pool IT foreign "+uuid.NewString())
	if err != nil {
		t.Fatalf("foreign workspace: %v", err)
	}
	mailbox := func(email string) uuid.UUID {
		mb, err := q.CreateMailbox(ctx, gen.CreateMailboxParams{
			WorkspaceID: ws.ID, Provider: "smtp", Email: email, DisplayName: email,
			SmtpHost: "smtp.acme.test", SmtpPort: 587, SmtpUsername: email,
			ImapHost: "imap.acme.test", ImapPort: 993, ImapUsername: email,
			SecretCiphertext: ct, DailyCap: 100, MinIntervalSeconds: 0,
			RampEnabled: false, RampStartCap: 5, RampDays: 30,
		})
		if err != nil {
			t.Fatalf("mailbox %s: %v", email, err)
		}
		return mb.ID
	}
	a := mailbox("a-" + uuid.NewString() + "@acme.test")
	b := mailbox("b-" + uuid.NewString() + "@acme.test")

	lst, err := q.CreateList(ctx, gen.CreateListParams{WorkspaceID: ws.ID, Name: "L"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	cam, err := q.CreateCampaign(ctx, gen.CreateCampaignParams{
		WorkspaceID: ws.ID, Name: "Pool", MailboxID: a, ListID: lst.ID,
		Subject: "Hi", BodyText: "Hello",
	})
	if err != nil {
		t.Fatalf("campaign: %v", err)
	}
	// Two steps so a follow-up exists to prove the thread keeps its mailbox.
	for _, order := range []int32{1, 2} {
		if _, err := q.CreateStep(ctx, gen.CreateStepParams{
			WorkspaceID: ws.ID, CampaignID: cam.ID, StepOrder: order, DelaySeconds: 0,
			Subject: "Hi", BodyText: "Hello",
		}); err != nil {
			t.Fatalf("step %d: %v", order, err)
		}
	}
	// CreateCampaign does not go through the campaign store, so no pool rows and
	// no send windows exist — which is exactly the unconfigured shape these tests
	// want to start from.
	core := New(pool, itKeyring(t, q), []byte("0123456789abcdef0123456789abcdef"),
		"https://app.test", mail.GoogleOAuth{}, mail.MicrosoftOAuth{},
		[]byte("warmup-secret-0123456789abcdef"), warmup.NewStaticLibrary())

	return ctx, poolFixture{
		core: core, pool: pool, q: q, ws: ws.ID, foreignWS: foreign.ID,
		campaignID: cam.ID, mailboxA: a, mailboxB: b,
	}
}

// enroll creates one contact + active enrollment and returns the enrollment id.
func (f poolFixture) enroll(t *testing.T, ctx context.Context) uuid.UUID {
	t.Helper()
	c, err := f.q.UpsertContact(ctx, gen.UpsertContactParams{
		WorkspaceID: f.ws, Email: "c-" + uuid.NewString() + "@x.test", FirstName: "C",
	})
	if err != nil {
		t.Fatalf("contact: %v", err)
	}
	var id uuid.UUID
	if err := f.pool.QueryRow(ctx,
		`INSERT INTO sequence_enrollments (workspace_id, campaign_id, contact_id, next_due_at)
		 VALUES ($1,$2,$3, now()) RETURNING id`, f.ws, f.campaignID, c.ID).Scan(&id); err != nil {
		t.Fatalf("enrollment: %v", err)
	}
	return id
}

// addSender puts one mailbox in the campaign's pool.
func (f poolFixture) addSender(t *testing.T, ctx context.Context, mailboxID uuid.UUID, weight int32, enabled bool) {
	t.Helper()
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO campaign_senders (workspace_id, campaign_id, mailbox_id, weight, enabled)
		 VALUES ($1,$2,$3,$4,$5)`, f.ws, f.campaignID, mailboxID, weight, enabled); err != nil {
		t.Fatalf("pool row: %v", err)
	}
}

// fillToCap records `n` sent rows today for a mailbox, so its remaining capacity
// drops. A distinct contact per row keeps the (campaign,contact,step) index happy.
func (f poolFixture) fillToCap(t *testing.T, ctx context.Context, mailboxID uuid.UUID, n int) {
	t.Helper()
	for range n {
		c, err := f.q.UpsertContact(ctx, gen.UpsertContactParams{
			WorkspaceID: f.ws, Email: "filler-" + uuid.NewString() + "@x.test", FirstName: "F",
		})
		if err != nil {
			t.Fatalf("filler contact: %v", err)
		}
		if _, err := f.pool.Exec(ctx,
			`INSERT INTO sends (workspace_id, campaign_id, contact_id, mailbox_id, to_email, status, sent_at)
			 VALUES ($1,$2,$3,$4,'filler@x.test','sent', now())`,
			f.ws, f.campaignID, c.ID, mailboxID); err != nil {
			t.Fatalf("filler send: %v", err)
		}
	}
}

func (f poolFixture) storedMailbox(t *testing.T, ctx context.Context, enrollmentID uuid.UUID) uuid.UUID {
	t.Helper()
	stored, err := f.q.GetEnrollmentMailbox(ctx, gen.GetEnrollmentMailboxParams{
		ID: enrollmentID, WorkspaceID: f.ws,
	})
	if err != nil {
		t.Fatalf("GetEnrollmentMailbox: %v", err)
	}
	if !stored.Valid {
		return uuid.Nil
	}
	return stored.Bytes
}

func (f poolFixture) setRotationMode(t *testing.T, ctx context.Context, mode string) {
	t.Helper()
	if err := f.q.SetCampaignRotationMode(ctx, gen.SetCampaignRotationModeParams{
		ID: f.campaignID, WorkspaceID: f.ws, RotationMode: mode,
	}); err != nil {
		t.Fatalf("SetCampaignRotationMode: %v", err)
	}
}

// The invariant that matters most: a campaign with NO pool rows still sends, from
// campaigns.mailbox_id. This is the 000031 lesson — an invariant that depends on
// every writer remembering to seed a table is not an invariant.
func TestNoPoolRowsFallsBackToTheCampaignMailbox(t *testing.T) {
	ctx, f := setupPool(t)
	enrollmentID := f.enroll(t, ctx)

	job, err := f.core.GetStepSendJob(ctx, enrollmentID.String(), f.ws.String())
	if err != nil {
		t.Fatalf("GetStepSendJob: %v", err)
	}
	if job.Skip {
		t.Fatal("expected a send job for a pool-less campaign, got Skip")
	}
	if job.MailboxID != f.mailboxA.String() {
		t.Errorf("mailbox = %s, want campaigns.mailbox_id %s", job.MailboxID, f.mailboxA)
	}
	if job.EffectiveDailyCap <= 0 {
		t.Errorf("effective cap = %d, want the mailbox's own cap", job.EffectiveDailyCap)
	}
	// It is pinned even without a pool, so configuring one later cannot re-route a
	// thread already in flight.
	if got := f.storedMailbox(t, ctx, enrollmentID); got != f.mailboxA {
		t.Errorf("pinned mailbox = %s, want %s", got, f.mailboxA)
	}
}

// A mailbox already at today's cap must be skipped and a healthy peer used — the
// main practical win of pooling.
func TestCappedPoolMemberIsSkippedForItsPeer(t *testing.T) {
	ctx, f := setupPool(t)
	f.setRotationMode(t, ctx, rotation.ModeWeighted)
	f.addSender(t, ctx, f.mailboxA, 100, true) // heavily weighted, but capped below
	f.addSender(t, ctx, f.mailboxB, 1, true)
	f.fillToCap(t, ctx, f.mailboxA, 100) // daily_cap is 100

	enrollmentID := f.enroll(t, ctx)
	job, err := f.core.GetStepSendJob(ctx, enrollmentID.String(), f.ws.String())
	if err != nil {
		t.Fatalf("GetStepSendJob: %v", err)
	}
	if job.MailboxID != f.mailboxB.String() {
		t.Errorf("mailbox = %s, want the uncapped peer B %s", job.MailboxID, f.mailboxB)
	}
	// The transport must belong to the RESOLVED mailbox, not the campaign's.
	if job.SMTPUsername == "" || job.FromEmail == "" {
		t.Errorf("resolved sender carries no transport: from=%q user=%q", job.FromEmail, job.SMTPUsername)
	}
	if got := f.storedMailbox(t, ctx, enrollmentID); got != f.mailboxB {
		t.Errorf("pinned mailbox = %s, want B %s", got, f.mailboxB)
	}
	// The winner's rotation counters moved, in the claim's transaction.
	var assigned int64
	if err := f.pool.QueryRow(ctx,
		`SELECT assigned_count FROM campaign_senders WHERE campaign_id=$1 AND mailbox_id=$2`,
		f.campaignID, f.mailboxB).Scan(&assigned); err != nil {
		t.Fatalf("read counter: %v", err)
	}
	if assigned != 1 {
		t.Errorf("assigned_count = %d, want 1", assigned)
	}
}

// When EVERY member is capped the send defers exactly as a capped single-mailbox
// send does today: the job reports the pool's aggregate capacity as consumed. It
// must NOT pin a mailbox — nothing was chosen.
func TestAllCappedPoolDefersRatherThanErroring(t *testing.T) {
	ctx, f := setupPool(t)
	f.addSender(t, ctx, f.mailboxA, 1, true)
	f.addSender(t, ctx, f.mailboxB, 1, true)
	f.fillToCap(t, ctx, f.mailboxA, 100)
	f.fillToCap(t, ctx, f.mailboxB, 100)

	enrollmentID := f.enroll(t, ctx)
	job, err := f.core.GetStepSendJob(ctx, enrollmentID.String(), f.ws.String())
	if err != nil {
		t.Fatalf("GetStepSendJob must defer, not error: %v", err)
	}
	if job.Skip {
		t.Fatal("an exhausted pool must defer, not skip the enrollment")
	}
	if job.EffectiveDailyCap <= 0 || job.SentToday < job.EffectiveDailyCap {
		t.Errorf("cap/sent = %d/%d, want a cap-deferral (sent >= cap > 0)", job.EffectiveDailyCap, job.SentToday)
	}
	if got := f.storedMailbox(t, ctx, enrollmentID); got != uuid.Nil {
		t.Errorf("a deferred send pinned mailbox %s; nothing was chosen", got)
	}
}

// Every member disabled is the same verdict as every member capped: defer, don't
// silently fall back to a mailbox the operator excluded.
func TestFullyDisabledPoolDefersInsteadOfFallingBack(t *testing.T) {
	ctx, f := setupPool(t)
	f.addSender(t, ctx, f.mailboxB, 1, false)

	enrollmentID := f.enroll(t, ctx)
	job, err := f.core.GetStepSendJob(ctx, enrollmentID.String(), f.ws.String())
	if err != nil {
		t.Fatalf("GetStepSendJob: %v", err)
	}
	if job.SentToday < job.EffectiveDailyCap {
		t.Errorf("cap/sent = %d/%d, want a cap-deferral", job.EffectiveDailyCap, job.SentToday)
	}
	if got := f.storedMailbox(t, ctx, enrollmentID); got != uuid.Nil {
		t.Errorf("pinned %s from a fully-disabled pool", got)
	}
}

// The write-once claim: two workers resolving the same enrollment concurrently must
// converge on ONE mailbox, and the loser must read the winner's value rather than
// use its own selection.
func TestConcurrentAssignmentYieldsOneMailbox(t *testing.T) {
	ctx, f := setupPool(t)
	f.setRotationMode(t, ctx, rotation.ModeRoundRobin)
	f.addSender(t, ctx, f.mailboxA, 1, true)
	f.addSender(t, ctx, f.mailboxB, 1, true)
	enrollmentID := f.enroll(t, ctx)

	const workers = 8
	var wg sync.WaitGroup
	got := make([]string, workers)
	errs := make([]error, workers)
	for i := range workers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			job, err := f.core.GetStepSendJob(ctx, enrollmentID.String(), f.ws.String())
			errs[i], got[i] = err, job.MailboxID
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d: %v", i, err)
		}
	}
	for i, mailboxID := range got {
		if mailboxID != got[0] {
			t.Fatalf("worker %d resolved %s but worker 0 resolved %s", i, mailboxID, got[0])
		}
	}
	if stored := f.storedMailbox(t, ctx, enrollmentID); stored.String() != got[0] {
		t.Errorf("stored mailbox %s does not match the resolved %s", stored, got[0])
	}
	// Only the claim winner bumped a counter, so the pool's totals equal 1.
	var total int64
	if err := f.pool.QueryRow(ctx,
		`SELECT coalesce(sum(assigned_count),0) FROM campaign_senders WHERE campaign_id=$1`,
		f.campaignID).Scan(&total); err != nil {
		t.Fatalf("read counters: %v", err)
	}
	if total != 1 {
		t.Errorf("total assigned_count = %d, want exactly 1 for one contact", total)
	}
}

// A follow-up step is a reply in the same thread, so it must send from the mailbox
// that started the thread — even after the pool changed underneath it.
func TestFollowUpStepReusesThePinnedMailboxAfterThePoolChanges(t *testing.T) {
	ctx, f := setupPool(t)
	f.addSender(t, ctx, f.mailboxA, 1, true)
	enrollmentID := f.enroll(t, ctx)

	first, err := f.core.GetStepSendJob(ctx, enrollmentID.String(), f.ws.String())
	if err != nil {
		t.Fatalf("first step: %v", err)
	}
	if first.MailboxID != f.mailboxA.String() {
		t.Fatalf("first step mailbox = %s, want A %s", first.MailboxID, f.mailboxA)
	}

	// Advance the cursor to step 1 sent, then swap the pool to B only.
	if _, err := f.pool.Exec(ctx,
		`UPDATE sequence_enrollments SET current_step = 1 WHERE id = $1`, enrollmentID); err != nil {
		t.Fatalf("advance cursor: %v", err)
	}
	if _, err := f.pool.Exec(ctx,
		`DELETE FROM campaign_senders WHERE campaign_id = $1`, f.campaignID); err != nil {
		t.Fatalf("clear pool: %v", err)
	}
	f.addSender(t, ctx, f.mailboxB, 100, true)

	second, err := f.core.GetStepSendJob(ctx, enrollmentID.String(), f.ws.String())
	if err != nil {
		t.Fatalf("second step: %v", err)
	}
	if second.StepOrder != 2 {
		t.Fatalf("step order = %d, want 2", second.StepOrder)
	}
	if second.MailboxID != f.mailboxA.String() {
		t.Errorf("follow-up mailbox = %s, want the thread's mailbox A %s", second.MailboxID, f.mailboxA)
	}
	if second.FromEmail != first.FromEmail {
		t.Errorf("follow-up from = %q, want the thread's %q", second.FromEmail, first.FromEmail)
	}
}

// Deleting a mailbox mid-sequence clears the enrollment's pin (ON DELETE SET
// NULL) and CASCADEs its pool rows away. The thread's identity is gone, so the
// enrollment must STOP — never re-resolve onto another mailbox, which would send
// "Re:" from a new address referencing a Message-ID it never sent.
func TestDeletedMailboxStopsTheThreadInsteadOfRerouting(t *testing.T) {
	ctx, f := setupPool(t)
	// B alone in the pool, so step 1 deterministically pins B. B is NOT
	// campaigns.mailbox_id, which is what makes it deletable at all
	// (campaigns_mailbox_tenant_fkey is ON DELETE RESTRICT).
	f.addSender(t, ctx, f.mailboxB, 1, true)
	enrollmentID := f.enroll(t, ctx)

	// Step 1 pins the mailbox and delivers, so the thread has an identity.
	first, err := f.core.GetStepSendJob(ctx, enrollmentID.String(), f.ws.String())
	if err != nil {
		t.Fatalf("first step: %v", err)
	}
	if first.MailboxID != f.mailboxB.String() {
		t.Fatalf("first step mailbox = %s, want B %s", first.MailboxID, f.mailboxB)
	}
	if _, err := f.core.ClaimStepSend(ctx, first); err != nil {
		t.Fatalf("claim step 1: %v", err)
	}
	if err := f.core.MarkStepDelivered(ctx, first, "<step1@acme.test>"); err != nil {
		t.Fatalf("deliver step 1: %v", err)
	}
	if _, err := f.core.AdvanceStepCursor(ctx, first); err != nil {
		t.Fatalf("advance cursor: %v", err)
	}

	// Put A in the pool too, so a re-resolve would have somewhere to go — the whole
	// point is that it must NOT go there.
	f.addSender(t, ctx, f.mailboxA, 100, true)

	// The operator deletes the mailbox the thread was sending from: its pool row
	// CASCADEs away and the enrollment's pin is SET NULL.
	if _, err := f.pool.Exec(ctx, `DELETE FROM mailboxes WHERE id = $1`, f.mailboxB); err != nil {
		t.Fatalf("delete mailbox: %v", err)
	}
	if got := f.storedMailbox(t, ctx, enrollmentID); got != uuid.Nil {
		t.Fatalf("pin = %s after the delete, want cleared by ON DELETE SET NULL", got)
	}

	// The next advance must carry the stop, not a send.
	second, err := f.core.GetStepSendJob(ctx, enrollmentID.String(), f.ws.String())
	if err != nil {
		t.Fatalf("second step: %v", err)
	}
	if !second.MailboxRemoved {
		t.Fatalf("job = %+v, want MailboxRemoved", second)
	}
	if second.MailboxID != "" || second.SMTPPassword != nil || second.AccessToken != nil {
		t.Error("the stop job resolved a sender or opened a credential")
	}

	// Drive the stop through the same entry point the worker uses.
	if err := f.core.MarkStepStopped(ctx, enrollmentID.String(), f.ws.String(), "mailbox_removed"); err != nil {
		t.Fatalf("MarkStepStopped: %v", err)
	}
	var status string
	var reason *string
	if err := f.pool.QueryRow(ctx,
		`SELECT status, stop_reason FROM sequence_enrollments WHERE id = $1`, enrollmentID,
	).Scan(&status, &reason); err != nil {
		t.Fatalf("read enrollment: %v", err)
	}
	if status != "stopped" || reason == nil || *reason != "mailbox_removed" {
		t.Errorf("enrollment = %q/%v, want stopped/mailbox_removed", status, reason)
	}
	// No step-2 send row, and the surviving pool member (A) was never pinned.
	var step2 int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM sends WHERE campaign_id = $1 AND step_order = 2`, f.campaignID,
	).Scan(&step2); err != nil {
		t.Fatalf("count step-2 sends: %v", err)
	}
	if step2 != 0 {
		t.Errorf("step-2 send rows = %d, want none", step2)
	}
	if got := f.storedMailbox(t, ctx, enrollmentID); got != uuid.Nil {
		t.Errorf("pinned %s (the surviving member is A %s) after the mailbox was deleted", got, f.mailboxA)
	}
}

// A first send legitimately has no pin, so the deleted-mailbox rule must not fire
// on it — otherwise every new enrollment would stop instead of sending.
func TestUnpinnedFirstSendIsNotTreatedAsARemovedMailbox(t *testing.T) {
	ctx, f := setupPool(t)
	f.addSender(t, ctx, f.mailboxA, 1, true)
	enrollmentID := f.enroll(t, ctx)

	job, err := f.core.GetStepSendJob(ctx, enrollmentID.String(), f.ws.String())
	if err != nil {
		t.Fatalf("GetStepSendJob: %v", err)
	}
	if job.MailboxRemoved {
		t.Fatal("a pre-first-send enrollment was mistaken for a deleted mailbox")
	}
	if job.MailboxID != f.mailboxA.String() {
		t.Errorf("mailbox = %s, want A %s", job.MailboxID, f.mailboxA)
	}
}

// A foreign workspace id must resolve nothing, even with a valid enrollment id.
func TestSenderResolutionIsWorkspacePinned(t *testing.T) {
	ctx, f := setupPool(t)
	f.addSender(t, ctx, f.mailboxA, 1, true)
	enrollmentID := f.enroll(t, ctx)

	if _, err := f.core.GetStepSendJob(ctx, enrollmentID.String(), f.foreignWS.String()); err == nil {
		t.Fatal("a cross-tenant GetStepSendJob resolved a sender")
	}
	if got := f.storedMailbox(t, ctx, enrollmentID); got != uuid.Nil {
		t.Errorf("a cross-tenant call pinned mailbox %s", got)
	}
}
