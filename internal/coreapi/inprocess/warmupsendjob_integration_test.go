//go:build integration

package inprocess

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/crypto"
	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/keys"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/warmup"
)

// itMasterKey is the fixed 32-byte KEK/legacy key for these integration tests.
var itMasterKey = bytes.Repeat([]byte{9}, 32)

func itDSN() string {
	if v := os.Getenv("INROAD_DATABASE_URL"); v != "" {
		return v
	}
	return "postgres://inroad:inroad@localhost:5433/inroad?sslmode=disable"
}

func itKeyring(t *testing.T, q *gen.Queries) *crypto.Keyring {
	t.Helper()
	kp, err := crypto.NewLocalKeyProvider(itMasterKey)
	if err != nil {
		t.Fatalf("key provider: %v", err)
	}
	legacy, err := crypto.NewSealer(itMasterKey)
	if err != nil {
		t.Fatalf("legacy sealer: %v", err)
	}
	return crypto.NewKeyring(kp, keys.NewPgDEKStore(q), legacy)
}

// itMailbox seeds one smtp mailbox with a real sealed credential + enables warmup.
func itMailbox(t *testing.T, ctx context.Context, q *gen.Queries, sealer *crypto.Sealer, wsID uuid.UUID, email string) uuid.UUID {
	t.Helper()
	ct, err := sealer.Seal([]byte("smtp-app-password"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	mb, err := q.CreateMailbox(ctx, gen.CreateMailboxParams{
		WorkspaceID: wsID, Provider: "smtp", Email: email, DisplayName: email,
		SmtpHost: "smtp.acme.test", SmtpPort: 587, SmtpUsername: email,
		ImapHost: "imap.acme.test", ImapPort: 993, ImapUsername: email,
		SecretCiphertext: ct, DailyCap: 50, MinIntervalSeconds: 120,
		RampEnabled: false, RampStartCap: 5, RampDays: 30,
	})
	if err != nil {
		t.Fatalf("mailbox %s: %v", email, err)
	}
	if _, err := q.UpsertWarmupParticipant(ctx, gen.UpsertWarmupParticipantParams{
		MailboxID: mb.ID, WorkspaceID: wsID, StartVolume: 8, MaxVolume: 40, RampIncrement: 2, ReplyRate: 0.3,
	}); err != nil {
		t.Fatalf("participant %s: %v", email, err)
	}
	return mb.ID
}

type warmupFixture struct {
	core coreapi.Client
	q    *gen.Queries
	ws1  uuid.UUID
	a    uuid.UUID // ws1 sender
	b    uuid.UUID // ws1 partner
	ws2  uuid.UUID
	c    uuid.UUID // ws2 (foreign) mailbox
}

func setupWarmup(t *testing.T) (context.Context, warmupFixture) {
	t.Helper()
	ctx := context.Background()
	if err := db.Migrate(itDSN()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := db.Connect(ctx, itDSN())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	q := gen.New(pool)
	sealer, err := crypto.NewSealer(itMasterKey)
	if err != nil {
		t.Fatalf("sealer: %v", err)
	}

	ws1, err := q.CreateWorkspace(ctx, "Warmup IT 1")
	if err != nil {
		t.Fatalf("ws1: %v", err)
	}
	ws2, err := q.CreateWorkspace(ctx, "Warmup IT 2")
	if err != nil {
		t.Fatalf("ws2: %v", err)
	}
	a := itMailbox(t, ctx, q, sealer, ws1.ID, "a@acme.test")
	b := itMailbox(t, ctx, q, sealer, ws1.ID, "b@acme.test")
	c := itMailbox(t, ctx, q, sealer, ws2.ID, "c@other.test")

	core := New(pool, itKeyring(t, q), []byte("0123456789abcdef0123456789abcdef"),
		"https://app.test", mail.GoogleOAuth{}, mail.MicrosoftOAuth{},
		[]byte("warmup-secret-0123456789abcdef"), warmup.NewStaticLibrary())

	return ctx, warmupFixture{core: core, q: q, ws1: ws1.ID, a: a, b: b, ws2: ws2.ID, c: c}
}

// TestPartnerSelectionStaysInWorkspaceAndAvoidsSelf proves GetWarmupSendJob pairs A
// only with its same-workspace partner B, never itself and never the foreign
// mailbox C.
func TestPartnerSelectionStaysInWorkspaceAndAvoidsSelf(t *testing.T) {
	ctx, f := setupWarmup(t)
	job, err := f.core.GetWarmupSendJob(ctx, f.a.String(), f.ws1.String())
	if err != nil {
		t.Fatalf("GetWarmupSendJob: %v", err)
	}
	if job.Skip {
		t.Fatalf("expected a send job, got Skip")
	}
	if job.ToMailbox != f.b.String() {
		t.Fatalf("partner = %s, want B=%s (never self %s / foreign %s)", job.ToMailbox, f.b, f.a, f.c)
	}
	if job.FromMailbox != f.a.String() {
		t.Fatalf("from = %s, want A=%s", job.FromMailbox, f.a)
	}
}

// TestPartnerSelectionSkipsWhenAlone proves a workspace with a single participant
// has no eligible partner and skips.
func TestPartnerSelectionSkipsWhenAlone(t *testing.T) {
	ctx, f := setupWarmup(t)
	job, err := f.core.GetWarmupSendJob(ctx, f.c.String(), f.ws2.String())
	if err != nil {
		t.Fatalf("GetWarmupSendJob: %v", err)
	}
	if !job.Skip {
		t.Fatalf("expected Skip for a lone participant, got partner %s", job.ToMailbox)
	}
}

// TestClaimIdempotencyAndThreadAdvance drives the claim lifecycle: a first claim
// wins, a re-claim of a fresh 'sending' skips, MarkWarmupSent advances the thread
// and bumps stats, and a post-sent claim recover-forwards (AlreadySent).
func TestClaimIdempotencyAndThreadAdvance(t *testing.T) {
	ctx, f := setupWarmup(t)
	job, err := f.core.GetWarmupSendJob(ctx, f.a.String(), f.ws1.String())
	if err != nil || job.Skip {
		t.Fatalf("GetWarmupSendJob: job=%+v err=%v", job, err)
	}

	// First claim wins.
	if out, err := f.core.ClaimWarmupSend(ctx, job); err != nil || out != coreapi.ClaimWon {
		t.Fatalf("first claim: out=%v err=%v, want Won", out, err)
	}
	// A second claim of the fresh 'sending' lease is skipped.
	if out, err := f.core.ClaimWarmupSend(ctx, job); err != nil || out != coreapi.ClaimSkip {
		t.Fatalf("re-claim fresh lease: out=%v err=%v, want Skip", out, err)
	}

	// Finalize the send: thread advances, root recorded, sent counter bumped.
	const msgID = "<warmup-1@acme.test>"
	if err := f.core.MarkWarmupSent(ctx, job, msgID); err != nil {
		t.Fatalf("MarkWarmupSent: %v", err)
	}

	th, err := f.q.GetOpenWarmupThread(ctx, gen.GetOpenWarmupThreadParams{
		WorkspaceID: f.ws1, SenderMailbox: f.a, PartnerMailbox: f.b,
	})
	if err != nil {
		t.Fatalf("read thread: %v", err)
	}
	if th.Turn != 1 {
		t.Fatalf("thread turn = %d, want 1 after one send", th.Turn)
	}
	if th.RootMessageID != msgID {
		t.Fatalf("root_message_id = %q, want %q (set on turn 0)", th.RootMessageID, msgID)
	}
	sent, err := f.q.GetWarmupSentToday(ctx, gen.GetWarmupSentTodayParams{MailboxID: f.a, WorkspaceID: f.ws1})
	if err != nil {
		t.Fatalf("read sent: %v", err)
	}
	if sent != 1 {
		t.Fatalf("sent today = %d, want 1", sent)
	}

	// A claim after the row is 'sent' recover-forwards rather than re-sending.
	if out, err := f.core.ClaimWarmupSend(ctx, job); err != nil || out != coreapi.ClaimAlreadySent {
		t.Fatalf("post-sent claim: out=%v err=%v, want AlreadySent", out, err)
	}

	// MarkWarmupSent again is idempotent: no double count.
	if err := f.core.MarkWarmupSent(ctx, job, msgID); err != nil {
		t.Fatalf("MarkWarmupSent (idempotent): %v", err)
	}
	sent, _ = f.q.GetWarmupSentToday(ctx, gen.GetWarmupSentTodayParams{MailboxID: f.a, WorkspaceID: f.ws1})
	if sent != 1 {
		t.Fatalf("sent today after re-finalize = %d, want 1 (no double count)", sent)
	}
}

// TestInsertWarmupThreadRejectsCrossTenant proves the self-enforcing
// INSERT ... SELECT writes zero rows when the sender mailbox does not belong to the
// workspace: a foreign (sender, workspace) pair yields pgx.ErrNoRows.
func TestInsertWarmupThreadRejectsCrossTenant(t *testing.T) {
	ctx, f := setupWarmup(t)
	// Sender C belongs to ws2; pairing it under ws1 must write nothing.
	_, err := f.q.InsertWarmupThread(ctx, gen.InsertWarmupThreadParams{
		WorkspaceID: f.ws1, SenderMailbox: f.c, PartnerMailbox: f.a,
		Subject: "x", ContentKey: "k",
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("cross-tenant insert err = %v, want pgx.ErrNoRows (zero rows)", err)
	}
}

// TestClaimWarmupSendFailsClosedCrossTenant proves a claim whose from_mailbox is not
// in the pinned workspace inserts nothing and resolves to Skip (never a phantom
// send row bound to a foreign mailbox).
func TestClaimWarmupSendFailsClosedCrossTenant(t *testing.T) {
	ctx, f := setupWarmup(t)
	job := coreapi.WarmupSendJob{
		WorkspaceID: f.ws1.String(),
		FromMailbox: f.c.String(), // foreign mailbox
		ToMailbox:   f.a.String(),
		ThreadID:    uuid.NewString(),
		SendID:      uuid.NewString(),
		Token:       "t",
	}
	// ThreadID references no real thread, but the self-enforcing INSERT ... SELECT
	// short-circuits on the foreign from_mailbox before any FK is evaluated (zero
	// rows selected), so the claim resolves to Skip without erroring.
	out, err := f.core.ClaimWarmupSend(ctx, job)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if out != coreapi.ClaimSkip {
		t.Fatalf("cross-tenant claim out = %v, want Skip", out)
	}
}
