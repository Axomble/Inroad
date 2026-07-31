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
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/platform/crypto"
	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// These integration tests exercise the delivery-claim state machine directly
// against Postgres (claim-before-send, migration 000015). Docker must be up.

var claimMasterKey = bytes.Repeat([]byte{9}, 32)

func claimDSN() string {
	if v := os.Getenv("INROAD_DATABASE_URL"); v != "" {
		return v
	}
	return "postgres://inroad:inroad@localhost:5433/inroad?sslmode=disable"
}

func claimConnect(t *testing.T) (*pgxpool.Pool, *gen.Queries) {
	t.Helper()
	if err := db.Migrate(claimDSN()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := db.Connect(context.Background(), claimDSN())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool, gen.New(pool)
}

type claimFixture struct {
	ws, foreignWS         uuid.UUID
	campaignID, contactID uuid.UUID
	mailboxID             uuid.UUID
	email                 string
}

// seedForClaim builds a workspace (+ a separate foreign workspace) with a
// mailbox, list, one contact, and a campaign — the FK parents a sends row needs.
func seedForClaim(t *testing.T, ctx context.Context, q *gen.Queries) claimFixture {
	t.Helper()
	sealer, err := crypto.NewSealer(claimMasterKey)
	if err != nil {
		t.Fatalf("sealer: %v", err)
	}
	ct, err := sealer.Seal([]byte("smtp-app-password"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	ws, err := q.CreateWorkspace(ctx, "Claim IT "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	foreign, err := q.CreateWorkspace(ctx, "Claim IT foreign "+uuid.NewString())
	if err != nil {
		t.Fatalf("foreign workspace: %v", err)
	}
	mb, err := q.CreateMailbox(ctx, gen.CreateMailboxParams{
		WorkspaceID: ws.ID, Provider: "smtp", Email: "from@acme.test", DisplayName: "Acme",
		SmtpHost: "smtp.acme.test", SmtpPort: 587, SmtpUsername: "from@acme.test",
		ImapHost: "imap.acme.test", ImapPort: 993, ImapUsername: "from@acme.test",
		SecretCiphertext: ct, DailyCap: 500, MinIntervalSeconds: 0,
		RampEnabled: false, RampStartCap: 5, RampDays: 30,
	})
	if err != nil {
		t.Fatalf("mailbox: %v", err)
	}
	lst, err := q.CreateList(ctx, gen.CreateListParams{WorkspaceID: ws.ID, Name: "L"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	email := "alice-" + uuid.NewString() + "@x.test"
	c, err := q.UpsertContact(ctx, gen.UpsertContactParams{WorkspaceID: ws.ID, Email: email, FirstName: "Alice"})
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
	return claimFixture{
		ws: ws.ID, foreignWS: foreign.ID,
		campaignID: cam.ID, contactID: c.ID, mailboxID: mb.ID, email: email,
	}
}

func stepClaimParams(fx claimFixture, ws uuid.UUID, order int) gen.ClaimStepSendParams {
	return gen.ClaimStepSendParams{
		ID:          deriveStepSendID(fx.campaignID, fx.contactID, order),
		WorkspaceID: ws, CampaignID: fx.campaignID, ContactID: fx.contactID, MailboxID: fx.mailboxID,
		ToEmail: fx.email, StepOrder: int32(order), ReferencesHeader: "",
		LeaseSeconds: claimLeaseSeconds,
	}
}

// makeStale ages a claim's lease so the reclaim branch fires deterministically
// without waiting real time.
func makeStale(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) {
	t.Helper()
	if _, err := pool.Exec(ctx, "UPDATE sends SET claimed_at = now() - interval '10 minutes' WHERE id = $1", id); err != nil {
		t.Fatalf("age lease: %v", err)
	}
}

func TestClaimStepSendStateMachine(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	fx := seedForClaim(t, ctx, q)
	p := stepClaimParams(fx, fx.ws, 1)
	wantID := p.ID

	// Fresh insert wins the claim.
	got, err := q.ClaimStepSend(ctx, p)
	if err != nil || got != wantID {
		t.Fatalf("fresh claim: id=%v err=%v (want %v)", got, err, wantID)
	}

	// A fresh 'sending' lease is owned by (this) worker: a second claim loses.
	if _, err := q.ClaimStepSend(ctx, p); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("fresh sending must not re-claim, got err=%v", err)
	}

	// A STALE 'sending' lease (crashed worker) is reclaimable.
	makeStale(t, ctx, pool, wantID)
	if got, err := q.ClaimStepSend(ctx, p); err != nil || got != wantID {
		t.Fatalf("stale reclaim: id=%v err=%v", got, err)
	}

	// Finalized 'sent' is terminal: never re-claimed, even if aged.
	if err := q.SetSendResult(ctx, gen.SetSendResultParams{
		ID: wantID, Status: "sent", MessageID: "<m@x>", Error: "", WorkspaceID: fx.ws,
	}); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	makeStale(t, ctx, pool, wantID)
	if _, err := q.ClaimStepSend(ctx, p); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("sent row must never re-claim, got err=%v", err)
	}
}

func TestClaimStepSendCrashRecovery(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	fx := seedForClaim(t, ctx, q)
	p := stepClaimParams(fx, fx.ws, 2)

	// Worker A claims and then "crashes" (never finalizes).
	if _, err := q.ClaimStepSend(ctx, p); err != nil {
		t.Fatalf("initial claim: %v", err)
	}
	// Within the lease, no other worker may reclaim.
	if _, err := q.ClaimStepSend(ctx, p); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("within lease must not reclaim, got %v", err)
	}
	// After the lease expires (clock advanced past it), the crashed send is
	// re-driven exactly once.
	makeStale(t, ctx, pool, p.ID)
	if got, err := q.ClaimStepSend(ctx, p); err != nil || got != p.ID {
		t.Fatalf("post-lease reclaim: id=%v err=%v", got, err)
	}
}

func TestClaimStepSendWorkspaceScoping(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	fx := seedForClaim(t, ctx, q)

	// Owner claims the row (order 3).
	owner := stepClaimParams(fx, fx.ws, 3)
	if _, err := q.ClaimStepSend(ctx, owner); err != nil {
		t.Fatalf("owner claim: %v", err)
	}
	makeStale(t, ctx, pool, owner.ID)

	// A foreign workspace claiming the SAME (campaign,contact,step) touches zero
	// rows: the reclaim WHERE is workspace-pinned.
	foreign := stepClaimParams(fx, fx.foreignWS, 3)
	if _, err := q.ClaimStepSend(ctx, foreign); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("foreign ws must claim zero rows, got err=%v", err)
	}

	// A foreign-workspace finalize also touches zero rows: the status stays
	// 'sending', unchanged by the cross-tenant write.
	if err := q.SetSendResult(ctx, gen.SetSendResultParams{
		ID: owner.ID, Status: "sent", MessageID: "<x>", Error: "", WorkspaceID: fx.foreignWS,
	}); err != nil {
		t.Fatalf("foreign finalize exec: %v", err)
	}
	var status string
	if err := pool.QueryRow(ctx, "SELECT status FROM sends WHERE id=$1", owner.ID).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != "sending" {
		t.Fatalf("foreign finalize must not change the row, got status=%q", status)
	}
}

func TestReserveMailboxSendSlotEnforcesSpacingAndWorkspace(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	fx := seedForClaim(t, ctx, q)
	if _, err := pool.Exec(ctx,
		"UPDATE mailboxes SET min_interval_seconds = 120 WHERE id = $1", fx.mailboxID,
	); err != nil {
		t.Fatalf("configure interval: %v", err)
	}

	owner := gen.ReserveMailboxSendSlotParams{ID: fx.mailboxID, WorkspaceID: fx.ws}
	if _, err := q.ReserveMailboxSendSlot(ctx, owner); err != nil {
		t.Fatalf("first reservation: %v", err)
	}
	if _, err := q.ReserveMailboxSendSlot(ctx, owner); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("reservation inside interval must defer, got %v", err)
	}
	foreign := gen.ReserveMailboxSendSlotParams{ID: fx.mailboxID, WorkspaceID: fx.foreignWS}
	if _, err := q.ReserveMailboxSendSlot(ctx, foreign); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("foreign workspace must not reserve mailbox, got %v", err)
	}

	if _, err := pool.Exec(ctx,
		"UPDATE mailboxes SET last_send_at = now() - interval '121 seconds' WHERE id = $1", fx.mailboxID,
	); err != nil {
		t.Fatalf("age reservation: %v", err)
	}
	if _, err := q.ReserveMailboxSendSlot(ctx, owner); err != nil {
		t.Fatalf("reservation after interval: %v", err)
	}
}

// insertQueuedSend inserts one direct-path send row in 'queued' (as EnqueueSends
// would), returning its id.
func insertQueuedSend(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fx claimFixture) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	// step_order defaults to 1 (NOT NULL) — as EnqueueSends relies on; the direct
	// path claims by id, so the value is immaterial here.
	err := pool.QueryRow(ctx,
		`INSERT INTO sends (workspace_id, campaign_id, contact_id, mailbox_id, to_email, status)
		 VALUES ($1,$2,$3,$4,$5,'queued') RETURNING id`,
		fx.ws, fx.campaignID, fx.contactID, fx.mailboxID, fx.email).Scan(&id)
	if err != nil {
		t.Fatalf("insert queued send: %v", err)
	}
	return id
}

func TestClaimSendDirectStateMachine(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	fx := seedForClaim(t, ctx, q)
	id := insertQueuedSend(t, ctx, pool, fx)
	p := gen.ClaimSendParams{ID: id, WorkspaceID: fx.ws, LeaseSeconds: claimLeaseSeconds}

	// queued -> sending (claim wins).
	if got, err := q.ClaimSend(ctx, p); err != nil || got != id {
		t.Fatalf("queued claim: id=%v err=%v", got, err)
	}
	// fresh sending -> lose.
	if _, err := q.ClaimSend(ctx, p); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("fresh sending must not re-claim, got %v", err)
	}
	// Release (retryable failure) expires the lease -> immediately reclaimable.
	if err := q.ReleaseSend(ctx, gen.ReleaseSendParams{ID: id, WorkspaceID: fx.ws}); err != nil {
		t.Fatalf("release: %v", err)
	}
	if got, err := q.ClaimSend(ctx, p); err != nil || got != id {
		t.Fatalf("reclaim after release: id=%v err=%v", got, err)
	}
	// Finalize -> terminal, never reclaimed.
	if err := q.SetSendResult(ctx, gen.SetSendResultParams{ID: id, Status: "sent", MessageID: "<m>", WorkspaceID: fx.ws}); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	makeStale(t, ctx, pool, id)
	if _, err := q.ClaimSend(ctx, p); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("sent row must never re-claim, got %v", err)
	}
}

func TestClaimSendDirectWorkspaceScoping(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	fx := seedForClaim(t, ctx, q)
	id := insertQueuedSend(t, ctx, pool, fx)

	// A foreign workspace cannot claim a 'queued' row it doesn't own.
	if _, err := q.ClaimSend(ctx, gen.ClaimSendParams{ID: id, WorkspaceID: fx.foreignWS, LeaseSeconds: claimLeaseSeconds}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("foreign ws must claim zero rows, got err=%v", err)
	}
	// The owner still can.
	if got, err := q.ClaimSend(ctx, gen.ClaimSendParams{ID: id, WorkspaceID: fx.ws, LeaseSeconds: claimLeaseSeconds}); err != nil || got != id {
		t.Fatalf("owner claim after foreign attempt: id=%v err=%v", got, err)
	}
}
