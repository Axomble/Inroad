//go:build integration

package sequence

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/mail"
)

// These integration tests drive AdvanceHandler against real Postgres with a
// FAILING Sender to prove the transient/permanent split at the SQL layer (until
// now only mock-tested): a transient failure releases the claim and leaves the
// cursor untouched (reclaimable), a permanent failure fails the row forward and
// advances the cursor. Docker must be up (Postgres :5433).

// capturingSender fails with err (if set) and records the OutboundJob so the
// allow_plaintext threading can be asserted end-to-end (column → job → transport).
type capturingSender struct {
	err  error
	n    int
	last mail.OutboundJob
}

func (s *capturingSender) Send(_ context.Context, tj mail.OutboundJob, _ mail.Message) (string, error) {
	s.n++
	s.last = tj
	if s.err != nil {
		return "", s.err
	}
	return "<cap@inroad>", nil
}

// runAdvance drives one advance and returns the handler error (unlike the advance
// helper, which fails the test on any error — the failing-sender cases expect one).
func runAdvance(core coreapi.Client, s Sender, enq Enqueuer, eid, ws string) error {
	b, _ := json.Marshal(map[string]string{"enrollment_id": eid, "workspace_id": ws})
	h := AdvanceHandler(core, s, enq, "https://app.test", []byte("0123456789abcdef0123456789abcdef"), nil)
	return h(context.Background(), asynq.NewTask("sequence:advance", b))
}

// stepSendRow reads the single step-1 send row's status + claimed_at for the
// (campaign, contact), workspace-pinned.
func stepSendRow(t *testing.T, ctx context.Context, pool *pgxpool.Pool, ws, campaign, contact uuid.UUID) (status string, claimedAt time.Time) {
	t.Helper()
	err := pool.QueryRow(ctx,
		`SELECT status, claimed_at FROM sends
		 WHERE workspace_id=$1 AND campaign_id=$2 AND contact_id=$3 AND step_order=1`,
		ws, campaign, contact).Scan(&status, &claimedAt)
	if err != nil {
		t.Fatalf("read send row: %v", err)
	}
	return status, claimedAt
}

func TestAdvanceTransientFailureReleasesAndDoesNotAdvance(t *testing.T) {
	ctx := context.Background()
	pool, q, closeFn := connect(t)
	defer closeFn()
	sealer := newSealer(t)

	fx := seedCampaign(t, ctx, pool, q, sealer, [][3]string{
		{"Hi {{first_name}}", "Hello", "0"},
		{"Follow up", "Ping", "0"},
	})
	ids, err := q.EnrollListMembers(ctx, gen.EnrollListMembersParams{ID: fx.campaignID, WorkspaceID: fx.ws})
	if err != nil || len(ids) != 1 {
		t.Fatalf("enroll: %v", err)
	}
	eid := ids[0].ID

	// A net timeout is classified Retryable → the handler releases the claim and
	// returns the error so asynq retries; the row must NOT be finalized/advanced.
	transient := &net.OpError{Op: "dial", Err: timeoutStub{}}
	snd, enq := &capturingSender{err: transient}, newITEnq()
	if err := runAdvance(fx.core, snd, enq, eid.String(), fx.ws.String()); err == nil {
		t.Fatal("transient failure must return an error so asynq retries")
	}

	status, claimedAt := stepSendRow(t, ctx, pool, fx.ws, fx.campaignID, fx.contactID)
	if status != "sending" {
		t.Fatalf("transient: row must stay 'sending' (released, reclaimable), got %q", status)
	}
	// Release sets claimed_at to the epoch, so the lease is immediately expired and
	// the retry can reclaim it.
	if claimedAt.Year() > 1971 {
		t.Fatalf("transient: claimed_at must be expired (epoch) so the row is reclaimable, got %v", claimedAt)
	}

	e, err := q.GetEnrollment(ctx, gen.GetEnrollmentParams{ID: eid, WorkspaceID: fx.ws})
	if err != nil {
		t.Fatalf("get enrollment: %v", err)
	}
	if e.CurrentStep != 0 || e.Status != "active" {
		t.Fatalf("transient: cursor must NOT advance, got step=%d status=%s", e.CurrentStep, e.Status)
	}
}

func TestAdvancePermanentFailureFailsForwardAndAdvances(t *testing.T) {
	ctx := context.Background()
	pool, q, closeFn := connect(t)
	defer closeFn()
	sealer := newSealer(t)

	// Single-step campaign so the permanent-failure fail-forward completes it.
	fx := seedCampaign(t, ctx, pool, q, sealer, [][3]string{{"Hi {{first_name}}", "Hello", "0"}})
	ids, err := q.EnrollListMembers(ctx, gen.EnrollListMembersParams{ID: fx.campaignID, WorkspaceID: fx.ws})
	if err != nil || len(ids) != 1 {
		t.Fatalf("enroll: %v", err)
	}
	eid := ids[0].ID

	// A 5xx-style error is NOT Retryable → fail-forward: finalize 'failed' and
	// advance the cursor (here: complete) so one bad step never wedges forever.
	snd, enq := &capturingSender{err: errors.New("smtp 550 no such user")}, newITEnq()
	if err := runAdvance(fx.core, snd, enq, eid.String(), fx.ws.String()); err != nil {
		t.Fatalf("permanent failure must fail-forward (no error), got %v", err)
	}

	status, _ := stepSendRow(t, ctx, pool, fx.ws, fx.campaignID, fx.contactID)
	if status != "failed" {
		t.Fatalf("permanent: row must be 'failed', got %q", status)
	}
	e, err := q.GetEnrollment(ctx, gen.GetEnrollmentParams{ID: eid, WorkspaceID: fx.ws})
	if err != nil {
		t.Fatalf("get enrollment: %v", err)
	}
	if e.CurrentStep != 1 || e.Status != "completed" {
		t.Fatalf("permanent: cursor must advance-forward, got step=%d status=%s", e.CurrentStep, e.Status)
	}
}

// TestAdvanceThreadsAllowPlaintextFromDB proves the persisted mailbox
// allow_plaintext column flows column → GetStepSendJob → StepSendJob →
// OutboundJob (MAJOR 2), end-to-end against Postgres.
func TestAdvanceThreadsAllowPlaintextFromDB(t *testing.T) {
	ctx := context.Background()
	pool, q, closeFn := connect(t)
	defer closeFn()
	sealer := newSealer(t)

	for _, allow := range []bool{true, false} {
		fx := seedCampaign(t, ctx, pool, q, sealer, [][3]string{{"Hi", "Hello", "0"}})
		if _, err := pool.Exec(ctx,
			`UPDATE mailboxes SET allow_plaintext=$1 WHERE workspace_id=$2`, allow, fx.ws); err != nil {
			t.Fatalf("set allow_plaintext: %v", err)
		}
		ids, err := q.EnrollListMembers(ctx, gen.EnrollListMembersParams{ID: fx.campaignID, WorkspaceID: fx.ws})
		if err != nil || len(ids) != 1 {
			t.Fatalf("enroll: %v", err)
		}
		snd, enq := &capturingSender{}, newITEnq()
		if err := runAdvance(fx.core, snd, enq, ids[0].ID.String(), fx.ws.String()); err != nil {
			t.Fatalf("advance: %v", err)
		}
		if snd.n != 1 {
			t.Fatalf("expected one send, got %d", snd.n)
		}
		if snd.last.AllowPlaintext != allow {
			t.Fatalf("OutboundJob.AllowPlaintext = %v, want %v (persisted policy must reach the transport)", snd.last.AllowPlaintext, allow)
		}
	}
}
