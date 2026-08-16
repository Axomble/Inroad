//go:build integration

package inprocess

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/crypto"
	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/dbtest"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/keys"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/warmup"
)

// itMasterKey is the fixed 32-byte KEK/legacy key for these integration tests.
var itMasterKey = bytes.Repeat([]byte{9}, 32)

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
//
// StartVolume is coupled to setupWarmup's clock pinning: warmupBusyDay uses this same
// value (8) as the conservative target when it picks a day with enough send headroom.
// Change one and change the other. An earlier note here claimed keeping StartVolume at
// or below the anti-stall floor (8) was sufficient — it is not. That floor only
// guarantees ONE send a day, and the fixture needs several, which is why the clock is
// pinned rather than the volume merely capped.
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
	// raw reaches tables that have no sqlc query of their own — warmup_observations
	// and warmup_state_transitions are written as side effects and read only by the
	// evaluator, so asserting on them needs direct SQL.
	raw gen.DBTX
	ws1 uuid.UUID
	a   uuid.UUID // ws1 sender
	b   uuid.UUID // ws1 partner
	ws2 uuid.UUID
	c   uuid.UUID // ws2 (foreign) mailbox
}

func setupWarmup(t *testing.T) (context.Context, warmupFixture) {
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

	// Pin the clock to a day the warmup day-shape actually grants sending volume on.
	// Without this the suite is calendar-dependent: warmup.EffectiveDailyVolume drops
	// weekends to a fraction of a weekday and skips ~4% of weekdays outright, so a
	// fixture that needs more than one send in a day passes on a Tuesday and fails on
	// a Saturday. (It did: TestRecordWarmupReceiptSenderAttribution went red on main
	// when CI ran on Saturday 2026-08-08.)
	// Both mailboxes: the day-shape jitter is seeded per mailbox id, so a day
	// generous to A can still skip B, and several tests drive sends from B.
	pinned := warmupBusyDay(t, a, b)
	if impl, ok := core.(client); ok {
		impl.now = func() time.Time { return pinned }
		core = impl
	} else {
		t.Fatalf("New returned %T, want inprocess.client — cannot pin the warmup clock", core)
	}

	return ctx, warmupFixture{core: core, q: q, raw: pool, ws1: ws1.ID, a: a, b: b, ws2: ws2.ID, c: c}
}

// warmupBusyDay returns noon UTC on the first day, from today forward, whose
// day-shape grants EVERY named mailbox at least warmupFixtureSends sends. It asks
// the REAL policy (warmup.EffectiveDailyVolume) rather than hardcoding a date, so
// it stays correct if the weekend/skip-day tuning changes.
//
// Every mailbox, not just the first, because EffectiveDailyVolume hashes the
// mailbox id into its skip-day decision: a day generous to A can skip B entirely.
// It took one argument until three tests across two slices started driving sends
// from B, each then failing on roughly one run in eight with nothing more useful
// than "job={Skip:true}". A fixture that guarantees volume for the mailbox a test
// does not use is not a guarantee.
//
// It uses the participant's START volume (8) as the target, which is a conservative
// lower bound: EffectiveDailyVolume is monotonic in target, and the ramp only ever
// raises it above the start, so a day that clears the bar at 8 clears it for real.
// Scanning FORWARD keeps the pinned instant at or after the participant's started_at,
// so ramp day counts stay non-negative.
func warmupBusyDay(t *testing.T, mailboxes ...uuid.UUID) time.Time {
	t.Helper()
	const startVolume = 8
	day := time.Now().UTC().Truncate(24 * time.Hour)
	for i := 0; i < 21; i++ {
		candidate := day.AddDate(0, 0, i).Add(12 * time.Hour) // noon: inside waking hours
		generous := true
		for _, mbx := range mailboxes {
			if warmup.EffectiveDailyVolume(startVolume, mbx.String(), candidate) < warmupFixtureSends {
				generous = false
				break
			}
		}
		if generous {
			return candidate
		}
	}
	t.Fatalf("no day in the next 3 weeks grants all of %v >= %d warmup sends each", mailboxes, warmupFixtureSends)
	return time.Time{}
}

// warmupFixtureSends is the per-day send headroom the warmup integration fixture
// needs. The most demanding test drives makeWarmupSend twice
// (TestRecordWarmupReceiptSenderAttribution); 4 leaves margin without requiring an
// unusually high-volume day.
const warmupFixtureSends = 4

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

// mustSealer builds the legacy sealer used to seal seeded mailbox credentials.
func mustSealer(t *testing.T) *crypto.Sealer {
	t.Helper()
	s, err := crypto.NewSealer(itMasterKey)
	if err != nil {
		t.Fatalf("sealer: %v", err)
	}
	return s
}

// forceReplyRate re-upserts a participant with a fixed reply_rate (1.0 → always
// reply, 0.0 → never), so a test can drive the reply-vs-new decision deterministically
// without depending on the seeded hash.
func forceReplyRate(t *testing.T, ctx context.Context, q *gen.Queries, mb, ws uuid.UUID, rate float32) {
	t.Helper()
	if _, err := q.UpsertWarmupParticipant(ctx, gen.UpsertWarmupParticipantParams{
		MailboxID: mb, WorkspaceID: ws, StartVolume: 8, MaxVolume: 40, RampIncrement: 2, ReplyRate: rate,
	}); err != nil {
		t.Fatalf("force reply_rate: %v", err)
	}
}

// seedRepliableThread opens a thread (sender→partner) and advances it to turn 1 so
// its opener is "sent" (root_message_id set) and a reply turn remains — the state
// SelectWarmupReplyPartner treats as repliable. Returns the recorded root Message-ID.
func seedRepliableThread(t *testing.T, ctx context.Context, q *gen.Queries, ws, sender, partner uuid.UUID) string {
	t.Helper()
	th, err := q.InsertWarmupThread(ctx, gen.InsertWarmupThreadParams{
		WorkspaceID: ws, SenderMailbox: sender, PartnerMailbox: partner,
		Subject: "seed", ContentKey: "seed-reply-thread",
	})
	if err != nil {
		t.Fatalf("seed thread: %v", err)
	}
	const root = "<seed-root@acme.test>"
	if err := q.AdvanceWarmupThread(ctx, gen.AdvanceWarmupThreadParams{
		ID: th.ID, WorkspaceID: ws, RootMessageID: root,
	}); err != nil {
		t.Fatalf("advance seed thread: %v", err)
	}
	return root
}

// TestReplyPrefersRepliablePartner proves the fix: when a reply is wanted and the
// sender has an OPEN, non-exhausted thread with partner B, GetWarmupSendJob replies
// to B — even though the recency-spread partner is the never-paired D (which sorts
// FIRST as the least-recently-active pick). Before the fix the reply would target D,
// find no open thread, and fall through to a new thread (under-realizing reply_rate).
func TestReplyPrefersRepliablePartner(t *testing.T) {
	ctx, f := setupWarmup(t)
	d := itMailbox(t, ctx, f.q, mustSealer(t), f.ws1, "d@acme.test")
	forceReplyRate(t, ctx, f.q, f.a, f.ws1, 1) // always reply
	root := seedRepliableThread(t, ctx, f.q, f.ws1, f.a, f.b)

	// The recency-spread partner is D (never paired → 'epoch'); B has a just-active
	// thread. Confirm that assumption so the test proves preference, not coincidence.
	sp, err := f.q.SelectWarmupPartner(ctx, gen.SelectWarmupPartnerParams{
		WorkspaceID: f.ws1, MailboxID: f.a, MaxPairSends: 100,
		CooldownSince: pgtype.Timestamptz{Time: time.Now().Add(-warmup.PairCooldown), Valid: true},
	})
	if err != nil {
		t.Fatalf("spread partner: %v", err)
	}
	if sp.MailboxID != d {
		t.Fatalf("recency-spread partner = %s, want D=%s (test needs B != spread partner)", sp.MailboxID, d)
	}

	job, err := f.core.GetWarmupSendJob(ctx, f.a.String(), f.ws1.String())
	if err != nil || job.Skip {
		t.Fatalf("GetWarmupSendJob: job=%+v err=%v", job, err)
	}
	if !job.IsReply {
		t.Fatalf("IsReply = false, want true (a reply was wanted and B is repliable)")
	}
	if job.ToMailbox != f.b.String() {
		t.Fatalf("reply partner = %s, want repliable B=%s (not spread partner D=%s)", job.ToMailbox, f.b, d)
	}
	if job.InReplyTo != root {
		t.Fatalf("InReplyTo = %q, want thread root %q", job.InReplyTo, root)
	}
	if job.References != root {
		t.Fatalf("References = %q, want thread root %q", job.References, root)
	}
}

// TestReplyFallsBackToNewThreadWhenNoRepliablePartner proves that when a reply is
// wanted but NO partner has an open repliable thread, GetWarmupSendJob falls back to
// the unchanged new-thread path on the recency-spread partner (a fresh thread, not a
// reply) rather than erroring or skipping.
func TestReplyFallsBackToNewThreadWhenNoRepliablePartner(t *testing.T) {
	ctx, f := setupWarmup(t)
	forceReplyRate(t, ctx, f.q, f.a, f.ws1, 1) // always want a reply

	job, err := f.core.GetWarmupSendJob(ctx, f.a.String(), f.ws1.String())
	if err != nil || job.Skip {
		t.Fatalf("GetWarmupSendJob: job=%+v err=%v", job, err)
	}
	if job.IsReply {
		t.Fatalf("IsReply = true, want false (no repliable thread exists → new thread)")
	}
	if job.ToMailbox != f.b.String() {
		t.Fatalf("new-thread partner = %s, want B=%s", job.ToMailbox, f.b)
	}
	if job.InReplyTo != "" {
		t.Fatalf("InReplyTo = %q, want empty on a new thread", job.InReplyTo)
	}
}

// TestReplyFallsBackWhenLatestThreadExhausted proves an EXHAUSTED thread (its turn
// reached the library maximum) is not treated as repliable: SelectWarmupReplyPartner
// excludes it, so a wanted reply falls back to a new thread with the same partner.
func TestReplyFallsBackWhenLatestThreadExhausted(t *testing.T) {
	ctx, f := setupWarmup(t)
	forceReplyRate(t, ctx, f.q, f.a, f.ws1, 1)

	th, err := f.q.InsertWarmupThread(ctx, gen.InsertWarmupThreadParams{
		WorkspaceID: f.ws1, SenderMailbox: f.a, PartnerMailbox: f.b,
		Subject: "seed", ContentKey: "seed-reply-thread",
	})
	if err != nil {
		t.Fatalf("seed thread: %v", err)
	}
	// Advance to the library max turn → exhausted for EVERY library thread, so the
	// coarse SQL bound (turn < max_turn) excludes it.
	for i := 0; i < warmup.MaxContentTurns(); i++ {
		if err := f.q.AdvanceWarmupThread(ctx, gen.AdvanceWarmupThreadParams{
			ID: th.ID, WorkspaceID: f.ws1, RootMessageID: "<exhausted@acme.test>",
		}); err != nil {
			t.Fatalf("advance: %v", err)
		}
	}

	job, err := f.core.GetWarmupSendJob(ctx, f.a.String(), f.ws1.String())
	if err != nil || job.Skip {
		t.Fatalf("GetWarmupSendJob: job=%+v err=%v", job, err)
	}
	if job.IsReply {
		t.Fatalf("IsReply = true, want false (latest thread exhausted → new thread)")
	}
	if job.ToMailbox != f.b.String() {
		t.Fatalf("partner = %s, want B=%s", job.ToMailbox, f.b)
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

// Acceptance criterion 7: "a stale pair lease cannot send after quarantine".
//
// The job is resolved while the sender is sendable, the sweep then contains it, and
// the claim must refuse. Expiry cannot deliver this — the lease has its full window
// left. Only re-reading the sender's CURRENT lane at claim can see it, which is why
// ClaimWarmupSend joins warmup_participants rather than trusting the caller.
func TestAQuarantineBetweenDecisionAndClaimStopsTheSend(t *testing.T) {
	ctx, f := setupWarmup(t)

	job, err := f.core.GetWarmupSendJob(ctx, f.a.String(), f.ws1.String())
	if err != nil || job.Skip {
		t.Fatalf("GetWarmupSendJob: job=%+v err=%v", job, err)
	}
	if job.IssuedLane == "" || job.LeaseExpiresAt.IsZero() {
		t.Fatalf("job carries no lease: lane=%q expires=%v", job.IssuedLane, job.LeaseExpiresAt)
	}
	if !job.LeaseExpiresAt.After(time.Now()) {
		t.Fatal("fixture is wrong: the lease must still be inside its window")
	}

	// The sweep quarantines the sender after the decision was made.
	if _, err := f.raw.Exec(ctx,
		`UPDATE warmup_participants SET lane = 'quarantine'
		  WHERE mailbox_id = $1 AND workspace_id = $2`, f.a, f.ws1); err != nil {
		t.Fatalf("quarantine sender: %v", err)
	}

	out, err := f.core.ClaimWarmupSend(ctx, job)
	if err != nil {
		t.Fatalf("ClaimWarmupSend: %v", err)
	}
	if out != coreapi.ClaimSkip {
		t.Fatalf("claim = %v, want ClaimSkip: a lease issued under %q must not fire after quarantine", out, job.IssuedLane)
	}
	var rows int
	if err := f.raw.QueryRow(ctx,
		`SELECT count(*) FROM warmup_sends WHERE id = $1 AND workspace_id = $2`,
		mustParseUUID(t, job.SendID), f.ws1).Scan(&rows); err != nil {
		t.Fatalf("count sends: %v", err)
	}
	if rows != 0 {
		t.Fatal("the refused claim still wrote a warmup_sends row")
	}
}

// An expired lease refuses even when nothing about the sender changed. This is the
// enqueue-to-pickup window: a backed-up or retrying queue firing a send long after
// its lane was decided.
func TestAnExpiredLeaseCannotClaim(t *testing.T) {
	ctx, f := setupWarmup(t)

	job, err := f.core.GetWarmupSendJob(ctx, f.a.String(), f.ws1.String())
	if err != nil || job.Skip {
		t.Fatalf("GetWarmupSendJob: job=%+v err=%v", job, err)
	}
	job.LeaseExpiresAt = time.Now().Add(-time.Minute) // the task sat in the queue

	out, err := f.core.ClaimWarmupSend(ctx, job)
	if err != nil {
		t.Fatalf("ClaimWarmupSend: %v", err)
	}
	if out != coreapi.ClaimSkip {
		t.Fatalf("claim = %v, want ClaimSkip for an expired lease", out)
	}
}

// The pair budget is one number for the two mailboxes, not one per direction. The
// old counter keyed on (from, to), so B->A spent nothing of A->B's allowance and
// real per-pair volume ran to about double the cap.
func TestThePairBudgetIsSymmetric(t *testing.T) {
	ctx, f := setupWarmup(t)
	makeWarmupSend(t, ctx, f) // A -> B

	forward, err := f.q.CountWarmupPairSendsToday(ctx, gen.CountWarmupPairSendsTodayParams{
		WorkspaceID: f.ws1, MailboxA: f.a, MailboxB: f.b,
	})
	if err != nil {
		t.Fatalf("count A,B: %v", err)
	}
	reverse, err := f.q.CountWarmupPairSendsToday(ctx, gen.CountWarmupPairSendsTodayParams{
		WorkspaceID: f.ws1, MailboxA: f.b, MailboxB: f.a,
	})
	if err != nil {
		t.Fatalf("count B,A: %v", err)
	}
	if forward != 1 || reverse != 1 {
		t.Fatalf("pair counts = %d forward / %d reverse, want 1 and 1: the budget must not depend on argument order", forward, reverse)
	}
}

func mustParseUUID(t *testing.T, s string) uuid.UUID {
	t.Helper()
	u, err := uuid.Parse(s)
	if err != nil {
		t.Fatalf("parse uuid %q: %v", s, err)
	}
	return u
}

// Criterion 7's actual mechanism, isolated from containment.
//
// The quarantine test above passes even with the lane COMPARISON removed, because
// the sealed-lane exclusion catches a quarantined sender on its own — so it proves
// containment, not lease staleness. Drift between two SENDABLE lanes is the case
// only the comparison can see: the sender is still perfectly allowed to send, it is
// simply not in the pool the decision was made for, and pairing across lanes is
// exactly what lane isolation forbids.
func TestALeaseDoesNotSurviveDriftBetweenSendableLanes(t *testing.T) {
	ctx, f := setupWarmup(t)

	job, err := f.core.GetWarmupSendJob(ctx, f.a.String(), f.ws1.String())
	if err != nil || job.Skip {
		t.Fatalf("GetWarmupSendJob: job=%+v err=%v", job, err)
	}
	if job.IssuedLane != warmup.LaneProbation {
		t.Fatalf("fixture assumes a probation sender, got %q", job.IssuedLane)
	}

	// Promoted between the decision and the claim. Still sendable — so the sealed
	// lane exclusion does NOT fire — but its pool changed, and the partner it was
	// matched with is a probation peer.
	if _, err := f.raw.Exec(ctx,
		`UPDATE warmup_participants SET lane = 'healthy'
		  WHERE mailbox_id = $1 AND workspace_id = $2`, f.a, f.ws1); err != nil {
		t.Fatalf("promote sender: %v", err)
	}

	out, err := f.core.ClaimWarmupSend(ctx, job)
	if err != nil {
		t.Fatalf("ClaimWarmupSend: %v", err)
	}
	if out != coreapi.ClaimSkip {
		t.Fatalf("claim = %v, want ClaimSkip: a lease issued under %q must not fire once the sender moved lanes", out, job.IssuedLane)
	}
}
