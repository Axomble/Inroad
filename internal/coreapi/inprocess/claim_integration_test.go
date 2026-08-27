//go:build integration

package inprocess

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/platform/crypto"
	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/dbtest"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// These integration tests exercise the delivery-claim state machine directly
// against Postgres (claim-before-send, migration 000015). Docker must be up.

var claimMasterKey = bytes.Repeat([]byte{9}, 32)

func claimConnect(t *testing.T) (*pgxpool.Pool, *gen.Queries) {
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

	// Fresh insert wins the claim, and reports itself as freshly inserted —
	// which is what separates a normal "won" from a "reclaimed" crashed lease
	// in inroad_send_claims_total.
	got, err := q.ClaimStepSend(ctx, p)
	if err != nil || got.ID != wantID {
		t.Fatalf("fresh claim: id=%v err=%v (want %v)", got.ID, err, wantID)
	}
	if !got.FreshlyInserted {
		t.Fatal("fresh insert must report freshly_inserted=true")
	}

	// A fresh 'sending' lease is owned by (this) worker: a second claim loses.
	if _, err := q.ClaimStepSend(ctx, p); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("fresh sending must not re-claim, got err=%v", err)
	}

	// A STALE 'sending' lease (crashed worker) is reclaimable.
	makeStale(t, ctx, pool, wantID)
	got, err = q.ClaimStepSend(ctx, p)
	if err != nil || got.ID != wantID {
		t.Fatalf("stale reclaim: id=%v err=%v", got.ID, err)
	}
	// A reclaim took over an EXISTING row, so it must NOT read as fresh: this
	// is the exact signal that makes a dying-worker rate visible.
	if got.FreshlyInserted {
		t.Fatal("stale reclaim must report freshly_inserted=false")
	}

	// Release (retryable failure, ReleaseStepSend) expires the lease so a
	// retry reclaims it immediately, without waiting out the lease.
	if err := q.ReleaseSend(ctx, gen.ReleaseSendParams{ID: wantID, WorkspaceID: fx.ws}); err != nil {
		t.Fatalf("release: %v", err)
	}
	if got, err := q.ClaimStepSend(ctx, p); err != nil || got.ID != wantID {
		t.Fatalf("reclaim after release: id=%v err=%v", got.ID, err)
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
	got, err := q.ClaimStepSend(ctx, p)
	if err != nil || got.ID != p.ID {
		t.Fatalf("post-lease reclaim: id=%v err=%v", got.ID, err)
	}
	if got.FreshlyInserted {
		t.Fatal("crash recovery is a reclaim, so freshly_inserted must be false")
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
