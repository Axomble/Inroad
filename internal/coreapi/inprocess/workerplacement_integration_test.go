//go:build integration

package inprocess

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/warmup"
)

// These integration tests exercise fleet F4 — SCORED placement — against
// Postgres, on top of the routing fixtures in workerrouting_integration_test.go.
// Docker must be up.
//
// They replace the F5 tier tests (workerriskband*_integration_test.go), and
// several of them assert the OPPOSITE of what those did. That is the change,
// not an accident: the risk band is derived from RECIPIENT-side evidence, the
// recipient never observes a worker's egress IP, so the band cannot predict how
// a worker's provider will treat it. It survives as one weak term in a score
// instead of a partition that could refuse a placement outright.

// enrollWithLane enrolls mb in warmup and forces its lane directly with raw
// SQL rather than routing through the health evaluator's state machine: these
// tests only need the LANE value RiskBandForLane reads, not a
// production-realistic evidence trail (mirrors sentinelpairing_integration_
// test.go's setLane, adapted to the plain pool/queries fixture this file
// uses instead of warmupFixture).
func enrollWithLane(t *testing.T, ctx context.Context, q *gen.Queries, pool *pgxpool.Pool, ws, mb uuid.UUID, lane string) {
	t.Helper()
	if _, err := q.UpsertWarmupParticipant(ctx, gen.UpsertWarmupParticipantParams{
		MailboxID: mb, WorkspaceID: ws, StartVolume: 5, MaxVolume: 20, RampIncrement: 1, ReplyRate: 0.2,
	}); err != nil {
		t.Fatalf("enroll warmup participant: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE warmup_participants SET lane = $3 WHERE workspace_id = $1 AND mailbox_id = $2`,
		ws, mb, lane); err != nil {
		t.Fatalf("set lane %s on %s: %v", lane, mb, err)
	}
}

// createAPIMailbox creates a Gmail/Graph mailbox. Its SMTP and IMAP ports MUST
// be zero: migration 000057 replaced the blanket port range checks with
// provider-aware ones, so a non-smtp mailbox carrying a port violates them.
// Placement prices these mailboxes differently from SMTP ones, so the provider
// terms cannot be tested with createRoutingMailbox's smtp-only fixture.
func createAPIMailbox(t *testing.T, ctx context.Context, q *gen.Queries, ws uuid.UUID, provider string) uuid.UUID {
	t.Helper()
	mb, err := q.CreateMailbox(ctx, gen.CreateMailboxParams{
		WorkspaceID: ws, Provider: provider, Email: "api-" + uuid.NewString() + "@x.test", DisplayName: "MB",
		SecretCiphertext: "ciphertext", DailyCap: 100, MinIntervalSeconds: 0,
		RampEnabled: false, RampStartCap: 5, RampDays: 30,
	})
	if err != nil {
		t.Fatalf("create %s mailbox: %v", provider, err)
	}
	return mb.ID
}

// seedAssignments pins n freshly created mailboxes of `provider` to `worker`
// under `band`, directly through the query rather than through
// AssignMailboxWorker. Direct seeding is the only way to build a fixture the
// scorer would not itself have produced — which is exactly what a test of the
// scorer's preferences needs.
func seedAssignments(t *testing.T, ctx context.Context, q *gen.Queries, ws uuid.UUID, worker, band, provider string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		var mb uuid.UUID
		if provider == "smtp" {
			mb = createRoutingMailbox(t, ctx, q, ws)
		} else {
			mb = createAPIMailbox(t, ctx, q, ws, provider)
		}
		if _, err := q.InsertMailboxWorkerAssignment(ctx, gen.InsertMailboxWorkerAssignmentParams{
			MailboxID: mb, WorkspaceID: ws, WorkerID: worker, Band: band, LiveSince: liveSinceNow(),
		}); err != nil {
			t.Fatalf("seed %s/%s/%s: %v", worker, band, provider, err)
		}
	}
}

// blockWorkerForProvider records the provider verdict the health gate reads: a
// refusal to talk to this worker's address, with nothing succeeding alongside
// it. Written through the real coreapi writer so the test exercises the same
// rows a worker's flusher produces.
func blockWorkerForProvider(t *testing.T, ctx context.Context, c client, workerID, provider string, okEvents int64) {
	t.Helper()
	counts := []coreapi.WorkerProviderSignalCount{
		{Provider: provider, Operation: "send", Reason: "blocked", Events: 3},
	}
	if okEvents > 0 {
		counts = append(counts, coreapi.WorkerProviderSignalCount{
			Provider: provider, Operation: "send", Reason: "ok", Events: okEvents,
		})
	}
	if err := c.RecordWorkerProviderSignals(ctx, coreapi.WorkerProviderSignals{
		WorkerID:    workerID,
		WindowStart: time.Now().Add(-5 * time.Minute),
		WindowEnd:   time.Now(),
		Counts:      counts,
	}); err != nil {
		t.Fatalf("record %s signals for %s: %v", provider, workerID, err)
	}
}

// TestAssignMailboxWorkerSelfHostBypassIgnoresBand is the single most important
// test in this file, and it is unchanged from fleet F5: with only ONE live
// worker (the self-host, RoleAll topology), a healthy and a degraded mailbox
// BOTH land on it without refusal. There is no second worker to place onto, so
// neither segregation nor scoring nor the health gate may apply — refusing here
// would silently stop self-host from sending.
func TestAssignMailboxWorkerSelfHostBypassIgnoresBand(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	c := routingClient(pool, q)
	resetRouting(t, ctx, pool)

	ws, err := q.CreateWorkspace(ctx, "Routing selfhost "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	if err := c.UpsertWorkerHeartbeat(ctx, "solo", "203.0.113.40", "hostname"); err != nil {
		t.Fatalf("heartbeat solo: %v", err)
	}

	healthy := createRoutingMailbox(t, ctx, q, ws.ID)
	enrollWithLane(t, ctx, q, pool, ws.ID, healthy, warmup.LaneHealthy)
	if got, err := c.AssignMailboxWorker(ctx, healthy.String(), ws.ID.String()); err != nil || got != "w:solo" {
		t.Fatalf("healthy mailbox on solo worker = %q err=%v, want w:solo", got, err)
	}

	degraded := createRoutingMailbox(t, ctx, q, ws.ID)
	enrollWithLane(t, ctx, q, pool, ws.ID, degraded, warmup.LaneQuarantine)
	got, err := c.AssignMailboxWorker(ctx, degraded.String(), ws.ID.String())
	if err != nil {
		t.Fatalf("degraded mailbox must NOT be refused when it is the only worker: %v", err)
	}
	if got != "w:solo" {
		t.Fatalf("degraded mailbox on solo worker = %q, want w:solo (self-host must be unaffected)", got)
	}
}

// TestAssignMailboxWorkerSelfHostBypassIgnoresAProviderBlock is the other half
// of the bypass, and the one fleet F4 newly puts at risk: the health gate must
// not reach a single-worker deployment either. A self-host whose only worker has
// been blocked by a provider has nowhere else to send from, so refusing would
// take it off the air over a signal it cannot act on.
func TestAssignMailboxWorkerSelfHostBypassIgnoresAProviderBlock(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	c := routingClient(pool, q)
	resetRouting(t, ctx, pool)

	ws, err := q.CreateWorkspace(ctx, "Routing selfhost-blocked "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	if err := c.UpsertWorkerHeartbeat(ctx, "solo-blocked", "203.0.113.41", "hostname"); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	blockWorkerForProvider(t, ctx, c, "solo-blocked", "smtp", 0)

	mb := createRoutingMailbox(t, ctx, q, ws.ID)
	got, err := c.AssignMailboxWorker(ctx, mb.String(), ws.ID.String())
	if err != nil {
		t.Fatalf("a blocked SOLE worker must still take the placement: %v", err)
	}
	if got != "w:solo-blocked" {
		t.Fatalf("assign = %q, want w:solo-blocked", got)
	}
}

// TestAssignMailboxWorkerPrefersHeadroomOverTheMatchingBand inverts fleet F5's
// TestAssignMailboxWorkerSegregatesByRiskBand on the SAME fixture. seg-a is the
// only degraded-committed worker and is heavily loaded; seg-b is lightly loaded
// and carries the other band. F5 sent the degraded mailbox to seg-a, paying any
// amount of imbalance for a band match. F4 sends it to seg-b: the band is a weak
// term and load is not.
func TestAssignMailboxWorkerPrefersHeadroomOverTheMatchingBand(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	c := routingClient(pool, q)
	resetRouting(t, ctx, pool)

	ws, err := q.CreateWorkspace(ctx, "Routing headroom-vs-band "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	for _, w := range []string{"seg-a", "seg-b"} {
		if err := c.UpsertWorkerHeartbeat(ctx, w, "203.0.113.10", "hostname"); err != nil {
			t.Fatalf("heartbeat %s: %v", w, err)
		}
	}
	seedAssignments(t, ctx, q, ws.ID, "seg-a", warmup.RiskBandDegraded, "smtp", 8)
	seedAssignments(t, ctx, q, ws.ID, "seg-b", warmup.RiskBandHealthy, "smtp", 1)

	degraded := createRoutingMailbox(t, ctx, q, ws.ID)
	enrollWithLane(t, ctx, q, pool, ws.ID, degraded, warmup.LaneQuarantine)
	if got, err := c.AssignMailboxWorker(ctx, degraded.String(), ws.ID.String()); err != nil || got != "w:seg-b" {
		t.Fatalf("degraded mailbox = %q err=%v, want w:seg-b — the band must not outweigh eight mailboxes of load", got, err)
	}
}

// TestAssignMailboxWorkerBandBreaksATieBetweenEqualWorkers is the other side of
// the same coin: weak is not zero. Two workers identical in load, provider and
// tenant, differing ONLY in the band they carry — the band decides, for free.
// The degraded worker is named first alphabetically, so the deterministic
// tie-break would pick the WRONG one if the band term contributed nothing.
func TestAssignMailboxWorkerBandBreaksATieBetweenEqualWorkers(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	c := routingClient(pool, q)
	resetRouting(t, ctx, pool)

	ws, err := q.CreateWorkspace(ctx, "Routing band-tiebreak "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	for _, w := range []string{"tb-a-degraded", "tb-b-healthy"} {
		if err := c.UpsertWorkerHeartbeat(ctx, w, "203.0.113.12", "hostname"); err != nil {
			t.Fatalf("heartbeat %s: %v", w, err)
		}
	}
	seedAssignments(t, ctx, q, ws.ID, "tb-a-degraded", warmup.RiskBandDegraded, "smtp", 4)
	seedAssignments(t, ctx, q, ws.ID, "tb-b-healthy", warmup.RiskBandHealthy, "smtp", 4)

	mb := createRoutingMailbox(t, ctx, q, ws.ID)
	enrollWithLane(t, ctx, q, pool, ws.ID, mb, warmup.LaneHealthy)
	if got, err := c.AssignMailboxWorker(ctx, mb.String(), ws.ID.String()); err != nil || got != "w:tb-b-healthy" {
		t.Fatalf("healthy mailbox = %q err=%v, want w:tb-b-healthy — the band decides when nothing else does", got, err)
	}
}

// TestAssignMailboxWorkerPlacesWhenEveryWorkerCarriesTheOtherBand replaces
// fleet F5's TestAssignMailboxWorkerRefusesWhenBandCapacityIsExhausted, on the
// same fixture, with the opposite expectation. Every live worker is committed to
// the OTHER band and none is idle: F5 returned ErrNoBandCapacity and the mailbox
// stopped sending. F4 places it, because a band mismatch is a preference and
// there is no preference worth not sending over.
func TestAssignMailboxWorkerPlacesWhenEveryWorkerCarriesTheOtherBand(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	c := routingClient(pool, q)
	resetRouting(t, ctx, pool)

	ws, err := q.CreateWorkspace(ctx, "Routing otherband "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	for _, w := range []string{"r-a", "r-b"} {
		if err := c.UpsertWorkerHeartbeat(ctx, w, "203.0.113.30", "hostname"); err != nil {
			t.Fatalf("heartbeat %s: %v", w, err)
		}
		seedAssignments(t, ctx, q, ws.ID, w, warmup.RiskBandHealthy, "smtp", 1)
	}

	degraded := createRoutingMailbox(t, ctx, q, ws.ID)
	enrollWithLane(t, ctx, q, pool, ws.ID, degraded, warmup.LaneQuarantine)
	got, err := c.AssignMailboxWorker(ctx, degraded.String(), ws.ID.String())
	if err != nil {
		t.Fatalf("a band mismatch must never refuse a placement: %v", err)
	}
	if got != "w:r-a" && got != "w:r-b" {
		t.Fatalf("assign = %q, want one of the live workers", got)
	}
	if worker, exists := storedAssignment(t, ctx, pool, degraded, ws.ID); !exists {
		t.Fatalf("placement must persist a row, got (%q, exists=false)", worker)
	}
}

// TestAssignMailboxWorkerAvoidsAWorkerTheProviderIsRefusing: health is the only
// hard gate. bl-a is EMPTY — it wins every other term outright — and is excluded
// anyway, because everything its provider has said to it lately was a refusal to
// talk to it.
func TestAssignMailboxWorkerAvoidsAWorkerTheProviderIsRefusing(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	c := routingClient(pool, q)
	resetRouting(t, ctx, pool)

	ws, err := q.CreateWorkspace(ctx, "Routing blocked "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	for _, w := range []string{"bl-a-blocked", "bl-b-loaded"} {
		if err := c.UpsertWorkerHeartbeat(ctx, w, "203.0.113.70", "hostname"); err != nil {
			t.Fatalf("heartbeat %s: %v", w, err)
		}
	}
	blockWorkerForProvider(t, ctx, c, "bl-a-blocked", "smtp", 0)
	seedAssignments(t, ctx, q, ws.ID, "bl-b-loaded", warmup.RiskBandHealthy, "smtp", 10)

	mb := createRoutingMailbox(t, ctx, q, ws.ID)
	if got, err := c.AssignMailboxWorker(ctx, mb.String(), ws.ID.String()); err != nil || got != "w:bl-b-loaded" {
		t.Fatalf("assign = %q err=%v, want w:bl-b-loaded — an empty but blocked worker is not a candidate", got, err)
	}
}

// TestAssignMailboxWorkerIgnoresABlockForADifferentProvider: the gate is per
// PROVIDER. Both workers are blocked by GMAIL; an SMTP mailbox has no reason to
// care, and a fleet-wide health verdict would have refused it.
func TestAssignMailboxWorkerIgnoresABlockForADifferentProvider(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	c := routingClient(pool, q)
	resetRouting(t, ctx, pool)

	ws, err := q.CreateWorkspace(ctx, "Routing other-provider "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	for _, w := range []string{"op-a", "op-b"} {
		if err := c.UpsertWorkerHeartbeat(ctx, w, "203.0.113.71", "hostname"); err != nil {
			t.Fatalf("heartbeat %s: %v", w, err)
		}
		blockWorkerForProvider(t, ctx, c, w, "gmail", 0)
	}

	mb := createRoutingMailbox(t, ctx, q, ws.ID) // smtp
	got, err := c.AssignMailboxWorker(ctx, mb.String(), ws.ID.String())
	if err != nil {
		t.Fatalf("an SMTP mailbox must not be refused over a GMAIL block: %v", err)
	}
	if got == "" {
		t.Fatal("assign returned the default queue despite two live workers")
	}

	// ...and a gmail mailbox on the same fleet IS refused, which is what proves
	// the first half was about the provider rather than about the gate being
	// inert.
	gmailMB := createAPIMailbox(t, ctx, q, ws.ID, "gmail")
	if _, err := c.AssignMailboxWorker(ctx, gmailMB.String(), ws.ID.String()); !errors.Is(err, coreapi.ErrNoEligibleWorker) {
		t.Fatalf("gmail mailbox on a gmail-blocked fleet: err = %v, want ErrNoEligibleWorker", err)
	}
}

// TestAssignMailboxWorkerKeepsPlacingOnAWorkerThatIsStillSucceeding: a worker
// being blocked AND still completing operations is degraded, not dead. Excluding
// it on the first refusal would take a fleet out of service on one provider's
// bad afternoon — the same failure mode as a hard capacity ceiling.
func TestAssignMailboxWorkerKeepsPlacingOnAWorkerThatIsStillSucceeding(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	c := routingClient(pool, q)
	resetRouting(t, ctx, pool)

	ws, err := q.CreateWorkspace(ctx, "Routing degraded-not-dead "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	for _, w := range []string{"dn-a", "dn-b"} {
		if err := c.UpsertWorkerHeartbeat(ctx, w, "203.0.113.72", "hostname"); err != nil {
			t.Fatalf("heartbeat %s: %v", w, err)
		}
		blockWorkerForProvider(t, ctx, c, w, "smtp", 40)
	}

	mb := createRoutingMailbox(t, ctx, q, ws.ID)
	got, err := c.AssignMailboxWorker(ctx, mb.String(), ws.ID.String())
	if err != nil {
		t.Fatalf("a worker still completing sends must stay eligible: %v", err)
	}
	if got == "" {
		t.Fatal("assign returned the default queue despite two live, still-working workers")
	}
}

// TestAssignMailboxWorkerRefusesOnlyWhenNoWorkerIsEligible: the one refusal
// scored placement can produce, and what it leaves behind — no queue, no
// persisted row, and a decision-log entry that names the cause without inventing
// a score comparison.
func TestAssignMailboxWorkerRefusesOnlyWhenNoWorkerIsEligible(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	c := routingClient(pool, q)
	resetRouting(t, ctx, pool)

	ws, err := q.CreateWorkspace(ctx, "Routing refuse "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	for _, w := range []string{"ref-a", "ref-b"} {
		if err := c.UpsertWorkerHeartbeat(ctx, w, "203.0.113.73", "hostname"); err != nil {
			t.Fatalf("heartbeat %s: %v", w, err)
		}
		blockWorkerForProvider(t, ctx, c, w, "smtp", 0)
	}

	mb := createRoutingMailbox(t, ctx, q, ws.ID)
	got, err := c.AssignMailboxWorker(ctx, mb.String(), ws.ID.String())
	if !errors.Is(err, coreapi.ErrNoEligibleWorker) {
		t.Fatalf("assign on a wholly blocked fleet: got=%q err=%v, want ErrNoEligibleWorker", got, err)
	}
	if got != "" {
		t.Fatalf("a refused placement must not return a queue, got %q", got)
	}
	if worker, exists := storedAssignment(t, ctx, pool, mb, ws.ID); exists {
		t.Fatalf("a refused placement must persist nothing, got worker_id=%q", worker)
	}

	entries := decisionsFor(t, ctx, q, mb, ws.ID)
	if len(entries) != 1 {
		t.Fatalf("recorded %d decisions for a refusal, want 1: %+v", len(entries), entries)
	}
	got0 := entries[0]
	if got0.Kind != "refused" {
		t.Errorf("kind = %q, want refused", got0.Kind)
	}
	if got0.WorkerID != nil {
		t.Errorf("worker_id = %q, want NULL — a refusal placed the mailbox nowhere", *got0.WorkerID)
	}
	if !strings.HasPrefix(got0.Reason, "forced:") {
		t.Errorf("reason = %q, want a forced reason — nothing was scored", got0.Reason)
	}
	if strings.Contains(got0.Reason, " over ") {
		t.Errorf("reason = %q reads as a score comparison that never happened", got0.Reason)
	}
	if !strings.Contains(got0.Reason, "smtp") {
		t.Errorf("reason = %q does not name the provider it refused for", got0.Reason)
	}
}

// TestAssignMailboxWorkerSpreadsOneTenantAcrossWorkers: blast radius. Two
// workers identical in load, provider and band — the only difference is WHOSE
// mailboxes they carry. Concentrating one tenant on one egress IP means one bad
// IP takes that tenant off the air entirely, so placement spreads. "br-a-mine"
// sorts first, so a blast-radius term that measured nothing would pick it.
func TestAssignMailboxWorkerSpreadsOneTenantAcrossWorkers(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	c := routingClient(pool, q)
	resetRouting(t, ctx, pool)

	mine, err := q.CreateWorkspace(ctx, "Routing blast mine "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	other, err := q.CreateWorkspace(ctx, "Routing blast other "+uuid.NewString())
	if err != nil {
		t.Fatalf("other workspace: %v", err)
	}
	for _, w := range []string{"br-a-mine", "br-b-theirs"} {
		if err := c.UpsertWorkerHeartbeat(ctx, w, "203.0.113.80", "hostname"); err != nil {
			t.Fatalf("heartbeat %s: %v", w, err)
		}
	}
	seedAssignments(t, ctx, q, mine.ID, "br-a-mine", warmup.RiskBandHealthy, "smtp", 6)
	seedAssignments(t, ctx, q, other.ID, "br-b-theirs", warmup.RiskBandHealthy, "smtp", 6)

	mb := createRoutingMailbox(t, ctx, q, mine.ID)
	if got, err := c.AssignMailboxWorker(ctx, mb.String(), mine.ID.String()); err != nil || got != "w:br-b-theirs" {
		t.Fatalf("assign = %q err=%v, want w:br-b-theirs — this tenant is already concentrated on br-a-mine", got, err)
	}
}

// TestAssignMailboxWorkerPrefersTheWorkerCarryingFewerOfTheSameProvider:
// provider crowding, the term the per-worker provider signals exist to inform.
// Both workers carry 12 API mailboxes, so their weighted load is identical; only
// WHICH provider those mailboxes authenticate to differs. A Gmail mailbox avoids
// the worker already authenticating twelve of them, because a provider
// rate-limits and challenges per source address.
func TestAssignMailboxWorkerPrefersTheWorkerCarryingFewerOfTheSameProvider(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	c := routingClient(pool, q)
	resetRouting(t, ctx, pool)

	ws, err := q.CreateWorkspace(ctx, "Routing crowding "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	for _, w := range []string{"pc-a-gmail", "pc-b-m365"} {
		if err := c.UpsertWorkerHeartbeat(ctx, w, "203.0.113.81", "hostname"); err != nil {
			t.Fatalf("heartbeat %s: %v", w, err)
		}
	}
	seedAssignments(t, ctx, q, ws.ID, "pc-a-gmail", warmup.RiskBandHealthy, "gmail", 12)
	seedAssignments(t, ctx, q, ws.ID, "pc-b-m365", warmup.RiskBandHealthy, "m365", 12)

	mb := createAPIMailbox(t, ctx, q, ws.ID, "gmail")
	if got, err := c.AssignMailboxWorker(ctx, mb.String(), ws.ID.String()); err != nil || got != "w:pc-b-m365" {
		t.Fatalf("gmail mailbox = %q err=%v, want w:pc-b-m365 — pc-a-gmail already authenticates 12 gmail mailboxes", got, err)
	}
}

// TestAssignMailboxWorkerPricesAPIMailboxesBelowSMTPOnes: a worker carrying 12
// Gmail mailboxes has more room than one carrying 12 SMTP mailboxes, because an
// API mailbox costs HTTPS requests over a pooled transport while an SMTP one
// costs a persistent IMAP connection plus a session per send. Counting both as
// "1 mailbox" would call this a tie and the tie-break would pick the SMTP
// worker, which sorts first. The incoming mailbox is m365, which NEITHER worker
// carries, so crowding cannot decide it.
func TestAssignMailboxWorkerPricesAPIMailboxesBelowSMTPOnes(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	c := routingClient(pool, q)
	resetRouting(t, ctx, pool)

	ws, err := q.CreateWorkspace(ctx, "Routing weight "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	for _, w := range []string{"pw-a-smtp", "pw-b-gmail"} {
		if err := c.UpsertWorkerHeartbeat(ctx, w, "203.0.113.82", "hostname"); err != nil {
			t.Fatalf("heartbeat %s: %v", w, err)
		}
	}
	seedAssignments(t, ctx, q, ws.ID, "pw-a-smtp", warmup.RiskBandHealthy, "smtp", 12)
	seedAssignments(t, ctx, q, ws.ID, "pw-b-gmail", warmup.RiskBandHealthy, "gmail", 12)

	mb := createAPIMailbox(t, ctx, q, ws.ID, "m365")
	if got, err := c.AssignMailboxWorker(ctx, mb.String(), ws.ID.String()); err != nil || got != "w:pw-b-gmail" {
		t.Fatalf("m365 mailbox = %q err=%v, want w:pw-b-gmail — twelve API mailboxes occupy a worker less than twelve SMTP ones", got, err)
	}
}

// TestAssignMailboxWorkerKeepsALiveIncumbentAfterItsLaneDegrades inverts fleet
// F5's TestAssignMailboxWorkerMigratesOnLaneDegradation on the same fixture. F5
// treated a band change like a dead worker and moved the mailbox. F4 does not:
// the band is derived from recipient-side evidence, the recipient never sees the
// worker's egress IP, so the move discarded a real (mailbox, IP) trust
// relationship to buy a segregation that predicts nothing. Whether a mailbox
// should move at all is rotation's decision, not the send path's.
func TestAssignMailboxWorkerKeepsALiveIncumbentAfterItsLaneDegrades(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	c := routingClient(pool, q)
	resetRouting(t, ctx, pool)

	ws, err := q.CreateWorkspace(ctx, "Routing lane-change "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	for _, w := range []string{"m-a", "m-b"} {
		if err := c.UpsertWorkerHeartbeat(ctx, w, "203.0.113.50", "hostname"); err != nil {
			t.Fatalf("heartbeat %s: %v", w, err)
		}
	}

	mb := createRoutingMailbox(t, ctx, q, ws.ID)
	enrollWithLane(t, ctx, q, pool, ws.ID, mb, warmup.LaneHealthy)
	if got, err := c.AssignMailboxWorker(ctx, mb.String(), ws.ID.String()); err != nil || got != "w:m-a" {
		t.Fatalf("initial healthy assign = %q err=%v, want w:m-a", got, err)
	}
	var firstAssignedAt time.Time
	if err := pool.QueryRow(ctx,
		"SELECT assigned_at FROM mailbox_worker_assignments WHERE mailbox_id = $1", mb).
		Scan(&firstAssignedAt); err != nil {
		t.Fatalf("read assigned_at: %v", err)
	}

	// Now make staying look as bad as it possibly can. m-a is loaded far past
	// its target and carries only the OTHER band; m-b is nearly empty and
	// carries the band the mailbox is about to move into. A re-pick would choose
	// m-b on every single term — headroom, blast radius, crowding and band
	// agreement — so if this mailbox stays, it stays because incumbency
	// outranks all of them, not because the score happened to agree.
	seedAssignments(t, ctx, q, ws.ID, "m-a", warmup.RiskBandHealthy, "smtp", 20)
	seedAssignments(t, ctx, q, ws.ID, "m-b", warmup.RiskBandDegraded, "smtp", 2)

	if _, err := pool.Exec(ctx,
		`UPDATE warmup_participants SET lane = $3 WHERE workspace_id = $1 AND mailbox_id = $2`,
		ws.ID, mb, warmup.LaneQuarantine); err != nil {
		t.Fatalf("degrade lane: %v", err)
	}

	for i := range 3 {
		got, err := c.AssignMailboxWorker(ctx, mb.String(), ws.ID.String())
		if err != nil || got != "w:m-a" {
			t.Fatalf("resolve %d after lane degradation = %q err=%v, want stable w:m-a", i, got, err)
		}
	}
	worker, exists := storedAssignment(t, ctx, pool, mb, ws.ID)
	if !exists || worker != "m-a" {
		t.Fatalf("stored assignment = (%q, exists=%t), want (m-a, true)", worker, exists)
	}
	// assigned_at untouched: the pin did not move, so the age of the (mailbox,
	// IP) relationship the provider has been watching survives the lane change.
	var nowAssignedAt time.Time
	if err := pool.QueryRow(ctx,
		"SELECT assigned_at FROM mailbox_worker_assignments WHERE mailbox_id = $1", mb).
		Scan(&nowAssignedAt); err != nil {
		t.Fatalf("re-read assigned_at: %v", err)
	}
	if !nowAssignedAt.Equal(firstAssignedAt) {
		t.Fatalf("assigned_at moved for a live incumbent: %s -> %s", firstAssignedAt, nowAssignedAt)
	}
}

// TestPlacementTreatsANonWarmupMailboxAsHealthy: a mailbox with no
// warmup_participants row is not enrolled in warmup at all, and the empty lane
// must read as the healthy band — opting out of warmup cannot cost a mailbox its
// placement. The two workers are otherwise identical and the DEGRADED one sorts
// first, so a lane lookup returning something other than "" would pick it.
func TestPlacementTreatsANonWarmupMailboxAsHealthy(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	c := routingClient(pool, q)
	resetRouting(t, ctx, pool)

	ws, err := q.CreateWorkspace(ctx, "Routing noparticipant "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	for _, w := range []string{"nd-degraded", "nh-healthy"} {
		if err := c.UpsertWorkerHeartbeat(ctx, w, "203.0.113.60", "hostname"); err != nil {
			t.Fatalf("heartbeat %s: %v", w, err)
		}
	}
	seedAssignments(t, ctx, q, ws.ID, "nd-degraded", warmup.RiskBandDegraded, "smtp", 4)
	seedAssignments(t, ctx, q, ws.ID, "nh-healthy", warmup.RiskBandHealthy, "smtp", 4)

	mb := createRoutingMailbox(t, ctx, q, ws.ID) // never enrolled in warmup
	if got, err := c.AssignMailboxWorker(ctx, mb.String(), ws.ID.String()); err != nil || got != "w:nh-healthy" {
		t.Fatalf("non-participant mailbox = %q err=%v, want w:nh-healthy (an absent lane reads as healthy)", got, err)
	}
}

// TestConcurrentPlacementsOfDifferentMailboxesAllLand replaces fleet F5's
// idle-promotion race test. That test guarded an invariant F4 deliberately
// dissolves — "an idle worker is never adopted into two bands at once" — since
// nothing about a worker becomes exclusive when a mailbox lands on it any more,
// and the advisory lock that enforced it is gone with the tier.
//
// What must still hold is what this asserts: many DIFFERENT mailboxes placing
// simultaneously all succeed, each ends up with exactly one row, and every row
// names a LIVE worker. A placement that read a fleet state one mailbox out of
// date is a slightly suboptimal placement, never a broken one.
func TestConcurrentPlacementsOfDifferentMailboxesAllLand(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	c := routingClient(pool, q)
	resetRouting(t, ctx, pool)

	ws, err := q.CreateWorkspace(ctx, "Routing concurrent-place "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	live := []string{"cp-a", "cp-b", "cp-c"}
	for _, w := range live {
		if err := c.UpsertWorkerHeartbeat(ctx, w, "203.0.113.90", "hostname"); err != nil {
			t.Fatalf("heartbeat %s: %v", w, err)
		}
	}

	const placements = 12
	mailboxes := make([]uuid.UUID, placements)
	for i := range mailboxes {
		mailboxes[i] = createRoutingMailbox(t, ctx, q, ws.ID)
		// Alternate bands, which is precisely the mix F5's lock existed to keep
		// off one worker and F4 is content to co-locate.
		lane := warmup.LaneHealthy
		if i%2 == 1 {
			lane = warmup.LaneQuarantine
		}
		enrollWithLane(t, ctx, q, pool, ws.ID, mailboxes[i], lane)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	got := make([]string, placements)
	errs := make([]error, placements)
	for i, mb := range mailboxes {
		wg.Add(1)
		go func(i int, mb uuid.UUID) {
			defer wg.Done()
			<-start
			got[i], errs[i] = c.AssignMailboxWorker(ctx, mb.String(), ws.ID.String())
		}(i, mb)
	}
	close(start)
	wg.Wait()

	liveQueues := map[string]bool{}
	for _, w := range live {
		liveQueues["w:"+w] = true
	}
	for i, err := range errs {
		if err != nil {
			t.Fatalf("placement %d: unexpected error: %v", i, err)
		}
		if !liveQueues[got[i]] {
			t.Fatalf("placement %d resolved %q, want one of the live workers", i, got[i])
		}
		// storedAssignment fails the test if a mailbox ended up with more than
		// one row.
		worker, exists := storedAssignment(t, ctx, pool, mailboxes[i], ws.ID)
		if !exists || "w:"+worker != got[i] {
			t.Fatalf("placement %d: stored worker %q (exists=%t) disagrees with the resolved queue %q",
				i, worker, exists, got[i])
		}
	}
}

// TestPlacementScalesOutOntoAFreshWorker is the property a capacity CEILING
// would break and an age ramp on capacity would break twice: every existing
// worker is far past its target, an operator adds one, and the next placement
// goes there. Nothing is refused on the way.
func TestPlacementScalesOutOntoAFreshWorker(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	c := routingClient(pool, q)
	resetRouting(t, ctx, pool)

	ws, err := q.CreateWorkspace(ctx, "Routing scale-out "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	for _, w := range []string{"so-a", "so-b"} {
		if err := c.UpsertWorkerHeartbeat(ctx, w, "203.0.113.93", "hostname"); err != nil {
			t.Fatalf("heartbeat %s: %v", w, err)
		}
		// Well past the 40-unit target: a ceiling would refuse from here on.
		seedAssignments(t, ctx, q, ws.ID, w, warmup.RiskBandHealthy, "smtp", 45)
	}

	saturated := createRoutingMailbox(t, ctx, q, ws.ID)
	if got, err := c.AssignMailboxWorker(ctx, saturated.String(), ws.ID.String()); err != nil || got == "" {
		t.Fatalf("a saturated fleet must still place, got %q err=%v", got, err)
	}

	if err := c.UpsertWorkerHeartbeat(ctx, "so-z-fresh", "203.0.113.94", "hostname"); err != nil {
		t.Fatalf("heartbeat fresh: %v", err)
	}
	// "so-z-fresh" sorts LAST, so it can only win on score.
	next := createRoutingMailbox(t, ctx, q, ws.ID)
	if got, err := c.AssignMailboxWorker(ctx, next.String(), ws.ID.String()); err != nil || got != "w:so-z-fresh" {
		t.Fatalf("assign after scaling out = %q err=%v, want w:so-z-fresh — adding a worker must relieve a full fleet", got, err)
	}
}

// storedBand reads the band recorded alongside a placement. Raw SQL for the same
// reason as storedAssignment: no query returns it any more, because no decision
// reads it back.
func storedBand(t *testing.T, ctx context.Context, pool *pgxpool.Pool, mailbox uuid.UUID) string {
	t.Helper()
	var band string
	if err := pool.QueryRow(ctx,
		"SELECT band FROM mailbox_worker_assignments WHERE mailbox_id = $1", mailbox).Scan(&band); err != nil {
		t.Fatalf("read band: %v", err)
	}
	return band
}

// TestPlacementRecordsTheBandItPlacedUnder: the band column is no longer a
// placement INPUT, but it is still the only record of what the mailbox looked
// like when it was placed, and the scorer reads other rows' copies of it. A
// placement that wrote the wrong band would quietly corrupt every later
// candidate's band-conflict count.
func TestPlacementRecordsTheBandItPlacedUnder(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	c := routingClient(pool, q)
	resetRouting(t, ctx, pool)

	ws, err := q.CreateWorkspace(ctx, "Routing stored-band "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	for _, w := range []string{"sb-a", "sb-b"} {
		if err := c.UpsertWorkerHeartbeat(ctx, w, "203.0.113.95", "hostname"); err != nil {
			t.Fatalf("heartbeat %s: %v", w, err)
		}
	}

	for _, tc := range []struct{ lane, want string }{
		{warmup.LaneHealthy, warmup.RiskBandHealthy},
		{warmup.LaneQuarantine, warmup.RiskBandDegraded},
	} {
		mb := createRoutingMailbox(t, ctx, q, ws.ID)
		enrollWithLane(t, ctx, q, pool, ws.ID, mb, tc.lane)
		if _, err := c.AssignMailboxWorker(ctx, mb.String(), ws.ID.String()); err != nil {
			t.Fatalf("assign %s: %v", tc.lane, err)
		}
		if got := storedBand(t, ctx, pool, mb); got != tc.want {
			t.Errorf("lane %s stored band %q, want %q", tc.lane, got, tc.want)
		}
	}
}

// TestResetRoutingLeavesNoLiveWorkerBehind guards the fixtures above rather than
// the code under test: every scoring test in this file assumes the fleet
// contains EXACTLY the workers it created, and a live worker or a stale
// `blocked` signal leaked from another test would silently change every score —
// a failure that reads as a bug in placement. resetRouting is what prevents
// that; this asserts it actually does.
func TestResetRoutingLeavesNoLiveWorkerBehind(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	resetRouting(t, ctx, pool)

	if n, err := q.CountLiveWorkers(ctx, liveSinceNow()); err != nil || n != 0 {
		t.Fatalf("live workers after reset = %d err=%v, want 0", n, err)
	}
	var signals int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM worker_provider_signals").Scan(&signals); err != nil {
		t.Fatalf("count signals: %v", err)
	}
	if signals != 0 {
		t.Fatalf("worker_provider_signals after reset = %d, want 0 — a stale block would make an unrelated test's worker ineligible", signals)
	}
}
