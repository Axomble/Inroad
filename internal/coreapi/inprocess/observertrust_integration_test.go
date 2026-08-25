//go:build integration

package inprocess

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	appwarmup "github.com/inroad/inroad/internal/app/warmup"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/warmup"
)

// Observer trust against real Postgres: the one axis in this subsystem that GATES.
//
// Placement is SENDER-attributed but RECIPIENT-observed. Invariant 52 binds an
// observation to a real send and re-proves the send<->recipient pair in SQL, but until
// now nothing questioned the recipient's own verdict — so one mailbox reporting
// everything it received as spam degraded every sender that mailed it. These tests
// cover both directions of that correction, because they fail in opposite ways:
// leaving the hole open lets one mailbox throttle a pool, and over-correcting makes
// every sender that mails a strict-but-honest observer look cleaner than it is.

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

// refreshSnapshots runs the production refresh statement with an explicit exclusion
// list, which is the seam the sweep binds.
func refreshSnapshots(t *testing.T, ctx context.Context, f warmupFixture, ws uuid.UUID, discounted []uuid.UUID) {
	t.Helper()
	if _, err := f.q.UpsertWarmupSignalSnapshotsForWorkspace(ctx, gen.UpsertWarmupSignalSnapshotsForWorkspaceParams{
		WorkspaceID: ws, DiscountedObservers: discounted,
	}); err != nil {
		t.Fatalf("refresh snapshots: %v", err)
	}
}

// A hostile observer must not be able to degrade the senders that mail it.
//
// The fixture is the attack in its plainest form: mailbox `hostile` junks all 30
// messages A sends it while `honest` — the same provider, so the same cohort — inboxes
// all 40 of its own. Pooled, A reads 30 spam of 70 (a Wilson lower bound above the
// 30% throttle band); with the hostile reports discounted A reads 0 of 40 and is a
// healthy mailbox, which it is.
//
// It also asserts the finding is SURFACED. Discarding evidence an operator cannot see
// is how a reputation engine quietly starts lying, so the exclusion and the disclosure
// are one feature and are tested as one.
func TestAHostileObserverStopsDegradingTheSendersItReports(t *testing.T) {
	ctx, f := setupWarmup(t)
	f = withWallClock(t, f)
	seedAuthPassing(t, ctx, f, f.ws1, "acme.test")

	hostile := itObserverMailbox(t, ctx, f, f.ws1, "hostile@acme.test")
	honest := itObserverMailbox(t, ctx, f, f.ws1, "honest@acme.test")
	seedObservedPlacements(t, ctx, f, f.ws1, f.a, hostile, 30, 0)
	seedObservedPlacements(t, ctx, f, f.ws1, f.a, honest, 0, 40)

	if err := f.core.EvaluateWarmupHealth(ctx); err != nil {
		t.Fatalf("EvaluateWarmupHealth: %v", err)
	}

	inbox, spam := snapshotPlacement(t, ctx, f, f.ws1, f.a)
	if spam != 0 {
		t.Errorf("A's snapshot carries %d spam placements, want 0: every one of them was reported by an "+
			"observer that junks everything it receives, and none of them is evidence about A", spam)
	}
	if inbox != 40 {
		t.Errorf("A's snapshot carries %d inbox placements, want the honest observer's 40: discounting one "+
			"observer must not touch anyone else's reports", inbox)
	}
	health, lane := participantAxes(t, ctx, f, f.a)
	if health != warmup.StateHealthy || lane != warmup.LaneHealthy {
		t.Fatalf("A = %s/%s, want healthy/healthy: 40 clean placements from a trusted observer qualify it, "+
			"and one mailbox must not be able to throttle a sender by junking its mail", health, lane)
	}

	// The operator-visible half: the same finding, with the arithmetic behind it.
	ov, err := appwarmup.NewService(appwarmup.NewPgStore(f.q)).GetOverview(ctx, f.ws1)
	if err != nil {
		t.Fatalf("GetOverview: %v", err)
	}
	if len(ov.DiscountedObservers) != 1 {
		t.Fatalf("discounted_observers = %+v, want exactly the hostile mailbox — evidence dropped without "+
			"being reported is evidence dropped in secret", ov.DiscountedObservers)
	}
	got := ov.DiscountedObservers[0]
	if got.ObserverMailboxID != hostile.String() {
		t.Errorf("discounted observer = %s, want the hostile mailbox %s", got.ObserverMailboxID, hostile)
	}
	if got.Cohort != observerCohortESP || got.Spam != 30 || got.Total != 30 || got.SpamRate != 1 {
		t.Errorf("arithmetic = %+v, want 30 of 30 spam in the %s cohort", got, observerCohortESP)
	}
	if got.Lift < warmup.ObserverSpamLift {
		t.Errorf("lift = %v, want at least ObserverSpamLift (%v)", got.Lift, warmup.ObserverSpamLift)
	}
}

// An EMPTY exclusion list must count exactly what the refresh counted before observer
// trust existed. That is not a nicety: it is the fallback the sweep takes when the
// stats read fails, so if an empty list changed anything, one failed query would
// silently rewrite a workspace's evidence.
//
// The nil case is asserted for the same reason and is the sharper one. `x <> ALL(NULL)`
// is NULL rather than true, so a caller that bound a NULL array against the obvious
// spelling of this predicate would drop EVERY observation that has an observer — a
// whole workspace's placement evidence gone, reading as a clean, unmeasured pool.
//
// The third case is the control: with the hostile id actually bound, the parameter has
// to do something, or the first two assertions would pass over a predicate that never
// matches anything.
func TestAnEmptyExclusionListChangesNothing(t *testing.T) {
	ctx, f := setupWarmup(t)
	hostile := itObserverMailbox(t, ctx, f, f.ws1, "hostile@acme.test")
	honest := itObserverMailbox(t, ctx, f, f.ws1, "honest@acme.test")
	seedObservedPlacements(t, ctx, f, f.ws1, f.a, hostile, 30, 0)
	seedObservedPlacements(t, ctx, f, f.ws1, f.a, honest, 0, 40)

	for _, tc := range []struct {
		name        string
		discounted  []uuid.UUID
		inbox, spam int
	}{
		{"empty", []uuid.UUID{}, 40, 30},
		// A nil slice binds as a NULL array. It must read as "exclude nobody", never as
		// "exclude everybody".
		{"nil", nil, 40, 30},
		{"hostile excluded", []uuid.UUID{hostile}, 40, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			refreshSnapshots(t, ctx, f, f.ws1, tc.discounted)
			inbox, spam := snapshotPlacement(t, ctx, f, f.ws1, f.a)
			if inbox != tc.inbox || spam != tc.spam {
				t.Fatalf("snapshot = %d inbox / %d spam, want %d / %d", inbox, spam, tc.inbox, tc.spam)
			}
		})
	}
}

// A STRICT observer is not a hostile one, and the difference is the whole reason this
// rule needs a lift rather than a rate.
//
// `strict` junks 40% of what it receives — twice its cohort, and well past
// MinObserverSpamRate — but its Microsoft peers junk 20%, so the lift is 2 and its
// reports stand. Excluding it would delete real spam evidence and make every sender
// that mails it read cleaner than it is, which is the worse of the two failure modes
// this slice can have.
//
// The fixture is pinned through the stats read first, so the test cannot pass because
// `strict` was quietly under the sample floor: it clears MinObserverSamples and
// MinObserverSpamRate, and is spared by the lift alone.
func TestAStrictButNormalObserverIsNotDiscounted(t *testing.T) {
	ctx, f := setupWarmup(t)
	strict := itObserverMailbox(t, ctx, f, f.ws1, "strict@acme.test")
	peer := itObserverMailbox(t, ctx, f, f.ws1, "peer@acme.test")
	seedObservedPlacements(t, ctx, f, f.ws1, f.a, strict, 8, 12) // 40% of 20
	seedObservedPlacements(t, ctx, f, f.ws1, f.a, peer, 10, 40)  // 20% of 50
	assertObserverStats(t, ctx, f, f.ws1, strict, 8, 20)

	if err := f.core.EvaluateWarmupHealth(ctx); err != nil {
		t.Fatalf("EvaluateWarmupHealth: %v", err)
	}
	if _, spam := snapshotPlacement(t, ctx, f, f.ws1, f.a); spam != 18 {
		t.Errorf("A's snapshot carries %d spam placements, want all 18: a strict observer reporting twice "+
			"its cohort's rate is evidence, not an outlier", spam)
	}
	assertNoDiscountedObservers(t, ctx, f, f.ws1)
}

// One workspace's observers must never discount another's evidence, and the risk here
// is subtler than an id crossing a tenancy boundary: ids are unguessable, but a
// COHORT BASELINE pooled across tenants would change who counts as an outlier.
//
// ws1 is the strict-but-normal fixture, spared because its own Microsoft peers junk
// 20%. ws2 is a spotless Microsoft pool of the same size. Pool the two and ws1's
// baseline collapses to about 4%, the strict observer's lift jumps past 3, and 8 real
// spam reports vanish from a ws1 sender's evidence — a mailbox in another tenant
// making this one look clean.
func TestObserverTrustIsWorkspacePinned(t *testing.T) {
	ctx, f := setupWarmup(t)
	strict := itObserverMailbox(t, ctx, f, f.ws1, "strict@acme.test")
	peer := itObserverMailbox(t, ctx, f, f.ws1, "peer@acme.test")
	seedObservedPlacements(t, ctx, f, f.ws1, f.a, strict, 8, 12)
	seedObservedPlacements(t, ctx, f, f.ws1, f.a, peer, 10, 40)

	// The other tenant: a large, spotless cohort on the same provider.
	foreign := itObserverMailbox(t, ctx, f, f.ws2, "clean@other.test")
	seedObservedPlacements(t, ctx, f, f.ws2, f.c, foreign, 0, 100)

	stats, err := f.q.ListWarmupObserverStats(ctx, f.ws1)
	if err != nil {
		t.Fatalf("ListWarmupObserverStats: %v", err)
	}
	for _, row := range stats {
		if uuid.UUID(row.ObserverMailboxID.Bytes) == foreign {
			t.Fatalf("ws1's observer stats include ws2's mailbox %s", foreign)
		}
	}
	if len(stats) != 2 {
		t.Fatalf("ws1 observer stats = %d rows, want exactly its own two observers", len(stats))
	}

	if err := f.core.EvaluateWarmupHealth(ctx); err != nil {
		t.Fatalf("EvaluateWarmupHealth: %v", err)
	}
	if _, spam := snapshotPlacement(t, ctx, f, f.ws1, f.a); spam != 18 {
		t.Errorf("A's snapshot carries %d spam placements, want 18: another tenant's clean mailboxes must "+
			"not dilute the cohort ws1's observers are judged against", spam)
	}
	assertNoDiscountedObservers(t, ctx, f, f.ws1)
}

// assertObserverStats pins one observer's row in the stats read, so a fixture that
// stopped clearing the detector's gates fails as a fixture rather than passing as a
// finding.
func assertObserverStats(t *testing.T, ctx context.Context, f warmupFixture, ws, observer uuid.UUID, spam, total int64) {
	t.Helper()
	rows, err := f.q.ListWarmupObserverStats(ctx, ws)
	if err != nil {
		t.Fatalf("ListWarmupObserverStats: %v", err)
	}
	for _, r := range rows {
		if uuid.UUID(r.ObserverMailboxID.Bytes) != observer {
			continue
		}
		if r.Spam != spam || r.Total != total {
			t.Fatalf("observer %s reported %d spam of %d, want %d of %d", observer, r.Spam, r.Total, spam, total)
		}
		if r.Total < int64(warmup.MinObserverSamples) {
			t.Fatalf("the fixture's observer has %d samples, under MinObserverSamples (%d): it would be "+
				"spared for having said too little, not for being normal", r.Total, warmup.MinObserverSamples)
		}
		if rate := float64(r.Spam) / float64(r.Total); rate < warmup.MinObserverSpamRate {
			t.Fatalf("the fixture's observer reports %.2f spam, under MinObserverSpamRate (%v): it would be "+
				"spared by the absolute floor rather than by the lift", rate, warmup.MinObserverSpamRate)
		}
		return
	}
	t.Fatalf("observer %s has no row in ws %s's stats", observer, ws)
}

// assertNoDiscountedObservers checks the operator-visible half agrees that nobody was
// excluded, through the whole read an operator triggers.
func assertNoDiscountedObservers(t *testing.T, ctx context.Context, f warmupFixture, ws uuid.UUID) {
	t.Helper()
	ov, err := appwarmup.NewService(appwarmup.NewPgStore(f.q)).GetOverview(ctx, ws)
	if err != nil {
		t.Fatalf("GetOverview: %v", err)
	}
	if len(ov.DiscountedObservers) != 0 {
		t.Errorf("discounted_observers = %+v, want none", ov.DiscountedObservers)
	}
}
