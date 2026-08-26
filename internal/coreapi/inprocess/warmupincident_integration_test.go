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

// THE guard on "this gates nothing" for correlated incidents (design §7), and the one
// property that has to hold against the real evaluator: an incident is an
// EXPLANATION, so nothing about a mailbox may change because one names it.
//
// Unlike slices A, B and C this needs TWO reasons, either sufficient on its own.
//
// Calibration. MinIncidentMembers, MinIncidentCohort and MinIncidentLift are
// unvalidated guesses. Tolerable for a sentence an operator reads and can dismiss;
// not tolerable for a threshold that withholds sending. That reason expires when real
// pools have been observed.
//
// Invariant 57, WHICH DOES NOT EXPIRE. The route dimension rests on destination_esp,
// and security.md invariant 57 records that axis as influenceable WITHIN a workspace:
// whoever controls a mailbox domain's MX controls which route its observations file
// under. An incident derived from it inherits the influenceability. So a later slice
// that gates on a fault domain must bind its evidence to something the attacker does
// not control — the way invariant 52 binds the placement axis — and cannot inherit
// "slice D proved the correlation is real".
//
// Breaking this test is therefore a deliberate decision to be made against data and a
// tenancy argument, not a bug to be fixed.

// guardParticipant connects one mailbox on the fixture's authenticated domain and
// enrolls it in the pool through the production upsert, so it starts where a real
// mailbox starts (probation, no evidence).
func guardParticipant(t *testing.T, ctx context.Context, f warmupFixture, local string) uuid.UUID {
	t.Helper()
	email := local + "@acme.test"
	mb, err := f.q.CreateMailbox(ctx, gen.CreateMailboxParams{
		WorkspaceID: f.ws1, Provider: "smtp", Email: email, DisplayName: local,
		SmtpHost: "smtp.acme.test", SmtpPort: 587, SmtpUsername: email,
		ImapHost: "imap.acme.test", ImapPort: 993, ImapUsername: email,
		SecretCiphertext: "ct", DailyCap: 50, MinIntervalSeconds: 0,
		RampEnabled: false, RampStartCap: 5, RampDays: 30,
	})
	if err != nil {
		t.Fatalf("mailbox %s: %v", email, err)
	}
	if _, err := f.q.UpsertWarmupParticipant(ctx, gen.UpsertWarmupParticipantParams{
		MailboxID: mb.ID, WorkspaceID: f.ws1,
		StartVolume: 8, MaxVolume: 40, RampIncrement: 2, ReplyRate: 0.3,
	}); err != nil {
		t.Fatalf("participant %s: %v", email, err)
	}
	return mb.ID
}

// seedSignedPlacements records n trusted placements attributed to `sender`, each
// carrying an explicit DKIM signing domain — through the production writer, so the
// observations are exactly the rows a receipt would leave.
func seedSignedPlacements(t *testing.T, ctx context.Context, f warmupFixture,
	sender, observer uuid.UUID, signingDomain, placement string, n int) {
	t.Helper()
	send := seedWarmupSendRow(t, ctx, f, sender, observer)
	observed := pgtype.Timestamptz{Time: time.Now(), Valid: true}
	for i := 0; i < n; i++ {
		if err := f.q.RecordWarmupPlacementObservation(ctx, gen.RecordWarmupPlacementObservationParams{
			WorkspaceID: f.ws1, WarmupSendID: send, RecipientMailbox: observer,
			ReceiptID: uuid.New(), Placement: placement, ObservedAt: observed,
			DkimDomain: signingDomain,
		}); err != nil {
			t.Fatalf("seed %s placement %d signed by %s: %v", placement, i, signingDomain, err)
		}
	}
}

// poolAxes snapshots every participant's two axes, keyed by mailbox, so "nothing
// moved" is asserted over the WHOLE pool rather than the two mailboxes a test
// happens to name.
func poolAxes(t *testing.T, ctx context.Context, f warmupFixture) map[uuid.UUID][2]string {
	t.Helper()
	rows, err := f.raw.Query(ctx,
		`SELECT mailbox_id, health_state, lane FROM warmup_participants WHERE workspace_id = $1`, f.ws1)
	if err != nil {
		t.Fatalf("read pool axes: %v", err)
	}
	defer rows.Close()
	out := map[uuid.UUID][2]string{}
	for rows.Next() {
		var id uuid.UUID
		var health, lane string
		if err := rows.Scan(&id, &health, &lane); err != nil {
			t.Fatalf("scan pool axes: %v", err)
		}
		out[id] = [2]string{health, lane}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate pool axes: %v", err)
	}
	return out
}

// Two mailboxes, same workspace, same domain, the same 25 clean placements observed at
// the same instant — one signing as the domain a real incident is concentrated in, the
// other signing as its own. Both must come out of the evaluator with the SAME health
// state and the SAME lane, and detection must move neither.
//
// The fixture ISOLATES the guard three ways, so it cannot pass for the wrong reason:
// the incident is asserted to actually exist (otherwise nothing is being guarded), the
// probes are asserted to be PROMOTED rather than merely equal (otherwise two unmeasured
// mailboxes would agree trivially), and the whole pool's axes are compared before and
// after the read (otherwise a write to some third mailbox would go unnoticed).
func TestDetectingIncidentsChangesNoHealthStateAndNoLane(t *testing.T) {
	ctx, f := setupWarmup(t)
	f = withWallClock(t, f)
	seedAuthPassing(t, ctx, f, f.ws1, "acme.test")

	// The cohort: three mailboxes signing as bad.test, two of them earning a real
	// degradation from the evaluator out of their own spam placements.
	probeInside := guardParticipant(t, ctx, f, "probe-inside")
	degradedOne := guardParticipant(t, ctx, f, "degraded-one")
	degradedTwo := guardParticipant(t, ctx, f, "degraded-two")
	// The comparison: identical evidence to probeInside, its own signing domain.
	probeOutside := guardParticipant(t, ctx, f, "probe-outside")

	seedSignedPlacements(t, ctx, f, probeInside, f.b, "bad.test", placementInbox, 25)
	seedSignedPlacements(t, ctx, f, probeOutside, f.b, "own.test", placementInbox, 25)
	seedSignedPlacements(t, ctx, f, degradedOne, f.b, "bad.test", placementSpam, 25)
	seedSignedPlacements(t, ctx, f, degradedTwo, f.b, "bad.test", placementSpam, 25)

	if err := f.core.EvaluateWarmupHealth(ctx); err != nil {
		t.Fatalf("EvaluateWarmupHealth: %v", err)
	}

	insideHealth, insideLane := participantAxes(t, ctx, f, probeInside)
	outsideHealth, outsideLane := participantAxes(t, ctx, f, probeOutside)
	if insideHealth != outsideHealth || insideLane != outsideLane {
		t.Fatalf("identical evidence decided differently: the mailbox inside the incident cohort is %s/%s, "+
			"the one outside it is %s/%s. Sharing a fault domain with two degrading mailboxes must not "+
			"change a health state or a lane — the detection constants are uncalibrated guesses, and the "+
			"route dimension is influenceable within a workspace (invariant 57)",
			insideHealth, insideLane, outsideHealth, outsideLane)
	}
	if insideHealth != warmup.StateHealthy || insideLane != warmup.LaneHealthy {
		t.Fatalf("the probes are %s/%s, want healthy/healthy: 25 qualified clean placements must still "+
			"qualify them, or this test could pass with both mailboxes unmeasured", insideHealth, insideLane)
	}

	before := poolAxes(t, ctx, f)

	// The read an operator actually triggers: GET /warmup/overview, whole path.
	overview, err := appwarmup.NewService(appwarmup.NewPgStore(f.q)).GetOverview(ctx, f.ws1)
	if err != nil {
		t.Fatalf("GetOverview: %v", err)
	}
	if len(overview.Incidents) != 1 {
		t.Fatalf("incidents = %+v, want exactly one on bad.test: with no incident detected this test "+
			"guards nothing", overview.Incidents)
	}
	got := overview.Incidents[0]
	if got.Dimension != "signing_domain" || got.Value != "bad.test" || got.DegradedInside != 2 {
		t.Fatalf("incident = %+v, want two degraded members of the bad.test signing cohort", got)
	}
	if len(got.MemberMailboxIDs) != 2 {
		t.Fatalf("members = %v, want the two degrading mailboxes", got.MemberMailboxIDs)
	}
	for _, member := range got.MemberMailboxIDs {
		if member != degradedOne.String() && member != degradedTwo.String() {
			t.Errorf("incident names %s, which is not one of the two degrading mailboxes", member)
		}
	}

	after := poolAxes(t, ctx, f)
	if len(after) != len(before) {
		t.Fatalf("the pool changed size across a read: %d participants before, %d after", len(before), len(after))
	}
	for mailbox, was := range before {
		if now := after[mailbox]; now != was {
			t.Errorf("mailbox %s moved from %s/%s to %s/%s across a READ: detecting a correlation must "+
				"decide nothing", mailbox, was[0], was[1], now[0], now[1])
		}
	}
}
