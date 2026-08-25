//go:build integration

package inprocess

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/warmup"
)

// Observer trust against real Postgres: measured, published, and applied to NOTHING.
//
// Placement is SENDER-attributed but RECIPIENT-observed. Invariant 52 binds an
// observation to a real send and re-proves the send<->recipient pair in SQL, but
// nothing questions the recipient's own verdict — so one mailbox reporting everything
// it received as spam degrades every sender that mailed it. That hole is still open.
//
// It was closed, briefly, by excluding a discounted observer's reports from the
// evidence the policy reads. A security audit found the cohort dilutable — an attacker
// who adds clean volume drags the peer baseline down until an HONEST observer clears
// the multiple, silencing the mailbox that would have reported their spam. Trading a
// hole that makes senders look worse than they are for one that makes them look better
// is the wrong direction in a reputation engine, so the exclusion came out and the
// verdict stayed. See security.md invariant 59.
//
// These tests therefore pin two things that fail in opposite ways: the detector must
// still NAME a hostile observer over real rows, and its reports must still COUNT —
// the second being a deliberate decision, not an oversight.

// observerCohortESP is the destination_esp every observation in these fixtures is
// filed under. It is derived from the RECIPIENT, so on the observer axis it names the
// OBSERVER's own receiving provider — which is the cohort a rate is compared against.
const observerCohortESP = "microsoft"

// itObserverMailbox connects one mailbox in a named workspace WITHOUT enrolling it in
// warmup. An observer needs no participant row: a mailbox's placement reports are
// evidence about the senders that mailed it whether or not it is warming itself, and
// leaving it out of the pool keeps these fixtures' health assertions about the sender
// alone.
func itObserverMailbox(t *testing.T, ctx context.Context, f warmupFixture, ws uuid.UUID, email string) uuid.UUID {
	t.Helper()
	mb, err := f.q.CreateMailbox(ctx, gen.CreateMailboxParams{
		WorkspaceID: ws, Provider: "smtp", Email: email, DisplayName: email,
		SmtpHost: "smtp.acme.test", SmtpPort: 587, SmtpUsername: email,
		ImapHost: "imap.acme.test", ImapPort: 993, ImapUsername: email,
		SecretCiphertext: "ct", DailyCap: 50, MinIntervalSeconds: 0,
		RampEnabled: false, RampStartCap: 5, RampDays: 30,
	})
	if err != nil {
		t.Fatalf("observer mailbox %s: %v", email, err)
	}
	return mb.ID
}

// seedObservedPlacements records `spam` spam and `inbox` inbox placements attributed
// to `sender` and observed by `observer`, through the PRODUCTION writer so the rows
// are exactly what a receipt leaves behind — including the observer binding and the
// destination_esp the cohort is formed on.
func seedObservedPlacements(t *testing.T, ctx context.Context, f warmupFixture,
	ws, sender, observer uuid.UUID, spam, inbox int) {
	t.Helper()
	send := seedWarmupSendRowIn(t, ctx, f, ws, sender, observer)
	observed := pgtype.Timestamptz{Time: time.Now(), Valid: true}
	record := func(placement string, n int) {
		for i := 0; i < n; i++ {
			if err := f.q.RecordWarmupPlacementObservation(ctx, gen.RecordWarmupPlacementObservationParams{
				WorkspaceID: ws, WarmupSendID: send, RecipientMailbox: observer,
				ReceiptID: uuid.New(), Placement: placement, ObservedAt: observed,
				DestinationEsp: observerCohortESP,
			}); err != nil {
				t.Fatalf("seed %s placement %d observed by %s: %v", placement, i, observer, err)
			}
		}
	}
	record(placementSpam, spam)
	record(placementInbox, inbox)
}

// snapshotPlacement reads one mailbox's materialized placement counters — the
// evidence the policy actually reads, rather than the observations behind it.
func snapshotPlacement(t *testing.T, ctx context.Context, f warmupFixture, ws, mailbox uuid.UUID) (inbox, spam int) {
	t.Helper()
	if err := f.raw.QueryRow(ctx,
		`SELECT placement_inbox, placement_spam FROM warmup_signal_snapshots
		  WHERE workspace_id = $1 AND mailbox_id = $2`, ws, mailbox).Scan(&inbox, &spam); err != nil {
		t.Fatalf("read snapshot for %s: %v", mailbox, err)
	}
	return inbox, spam
}

// refreshSnapshots runs the production refresh statement.
func refreshSnapshots(t *testing.T, ctx context.Context, f warmupFixture, ws uuid.UUID) {
	t.Helper()
	if _, err := f.q.UpsertWarmupSignalSnapshotsForWorkspace(ctx, ws); err != nil {
		t.Fatalf("refresh snapshots: %v", err)
	}
}

// observerVerdicts runs the production stats query and the detector over it — the
// disclosure path, end to end, with nothing applied.
func observerVerdicts(t *testing.T, ctx context.Context, f warmupFixture, ws uuid.UUID) []warmup.DiscountedObserver {
	t.Helper()
	rows, err := f.q.ListWarmupObserverStats(ctx, ws)
	if err != nil {
		t.Fatalf("observer stats: %v", err)
	}
	stats := make([]warmup.ObserverStats, 0, len(rows))
	for _, r := range rows {
		stats = append(stats, warmup.ObserverStats{
			ObserverMailboxID: uuid.UUID(r.ObserverMailboxID.Bytes).String(),
			Cohort:            r.DestinationEsp,
			Spam:              int(r.Spam),
			Total:             int(r.Total),
		})
	}
	return warmup.DiscountObservers(stats)
}

// The detector names a hostile observer over real rows: the production stats query,
// the production writer's rows, and the real cohort the observations were filed under.
//
// Three observers, not two: the peer floor needs a baseline of at least two OTHER
// mailboxes, because five clean observations from a single peer must not be allowed to
// condemn a full record.
func TestObserverStatsNameAHostileReporterOverRealRows(t *testing.T) {
	ctx, f := setupWarmup(t)
	ws := f.ws1
	sender := f.a
	hostile := itObserverMailbox(t, ctx, f, ws, "hostile@acme.test")
	honestA := itObserverMailbox(t, ctx, f, ws, "honest-a@acme.test")
	honestB := itObserverMailbox(t, ctx, f, ws, "honest-b@acme.test")

	seedObservedPlacements(t, ctx, f, ws, sender, hostile, 30, 0)
	seedObservedPlacements(t, ctx, f, ws, sender, honestA, 0, 40)
	seedObservedPlacements(t, ctx, f, ws, sender, honestB, 0, 40)

	got := observerVerdicts(t, ctx, f, ws)
	if len(got) != 1 {
		t.Fatalf("verdicts = %+v, want exactly one (the hostile observer)", got)
	}
	if got[0].ObserverMailboxID != hostile.String() {
		t.Errorf("named %s, want the hostile observer %s", got[0].ObserverMailboxID, hostile)
	}
	if got[0].Spam != 30 || got[0].Total != 30 {
		t.Errorf("record = %d/%d, want 30/30", got[0].Spam, got[0].Total)
	}
}

// And its reports STILL COUNT. This is the reversal, pinned.
//
// If this test starts failing because the reports were excluded, that is not a bug to
// fix — it is a deliberate decision being reversed, and security.md invariant 59 sets
// out what has to be true first: the cohort key bound to something the attacker does
// not control.
func TestAHostileObserversReportsStillCountAsEvidence(t *testing.T) {
	ctx, f := setupWarmup(t)
	ws := f.ws1
	sender := f.a
	hostile := itObserverMailbox(t, ctx, f, ws, "hostile@acme.test")
	honestA := itObserverMailbox(t, ctx, f, ws, "honest-a@acme.test")
	honestB := itObserverMailbox(t, ctx, f, ws, "honest-b@acme.test")

	seedObservedPlacements(t, ctx, f, ws, sender, hostile, 30, 0)
	seedObservedPlacements(t, ctx, f, ws, sender, honestA, 0, 40)
	seedObservedPlacements(t, ctx, f, ws, sender, honestB, 0, 40)

	// The detector has named it — the disclosure is live.
	if got := observerVerdicts(t, ctx, f, ws); len(got) != 1 {
		t.Fatalf("verdicts = %+v, want the hostile observer named; without that this test "+
			"proves nothing about whether a NAMED observer is applied", got)
	}

	refreshSnapshots(t, ctx, f, ws)

	inbox, spam := snapshotPlacement(t, ctx, f, ws, sender)
	if inbox != 80 || spam != 30 {
		t.Errorf("snapshot = %d inbox / %d spam, want 80/30 — the hostile observer's 30 "+
			"reports must still be evidence, because nothing acts on the verdict", inbox, spam)
	}
}

// A STRICT observer is not a hostile one. Junking a lot of what it receives is normal
// for some providers, and naming it would put a false verdict in front of an operator.
func TestAStrictButNormalObserverIsNotDiscounted(t *testing.T) {
	ctx, f := setupWarmup(t)
	ws := f.ws1
	strict := itObserverMailbox(t, ctx, f, ws, "strict@acme.test")
	peerA := itObserverMailbox(t, ctx, f, ws, "peer-a@acme.test")
	peerB := itObserverMailbox(t, ctx, f, ws, "peer-b@acme.test")

	// 40% against peers at 20%: past the absolute floor, and a lift of 2.
	seedObservedPlacements(t, ctx, f, ws, f.a, strict, 20, 30)
	seedObservedPlacements(t, ctx, f, ws, f.a, peerA, 10, 40)
	seedObservedPlacements(t, ctx, f, ws, f.a, peerB, 10, 40)

	if got := observerVerdicts(t, ctx, f, ws); len(got) != 0 {
		t.Errorf("a strict but normal observer was named: %+v", got)
	}
}

// One workspace's observers never appear in another's verdicts, and never form part of
// another's baseline.
func TestObserverTrustIsWorkspacePinned(t *testing.T) {
	ctx, f := setupWarmup(t)
	foreign := itObserverMailbox(t, ctx, f, f.ws2, "foreign@other.test")
	seedObservedPlacements(t, ctx, f, f.ws2, f.c, foreign, 90, 0)

	local := itObserverMailbox(t, ctx, f, f.ws1, "local@acme.test")
	seedObservedPlacements(t, ctx, f, f.ws1, f.a, local, 0, 40)

	for _, v := range observerVerdicts(t, ctx, f, f.ws1) {
		if v.ObserverMailboxID == foreign.String() {
			t.Errorf("ws1's verdicts named ws2's observer: %+v", v)
		}
	}
	rows, err := f.q.ListWarmupObserverStats(ctx, f.ws1)
	if err != nil {
		t.Fatalf("observer stats: %v", err)
	}
	for _, r := range rows {
		if uuid.UUID(r.ObserverMailboxID.Bytes) == foreign {
			t.Errorf("ws1's observer stats include ws2's mailbox %s", foreign)
		}
	}
}
