//go:build integration

package warmup

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db/gen"
	pwarmup "github.com/inroad/inroad/internal/platform/warmup"
)

// Correlated incidents over real rows. Detection itself is a pure fold with its own
// unit tests; what only Postgres can prove is the PROJECTION — that the query hands
// the fold the right participants with the right dimension values, over the same mail
// the rates beside them describe.

// incidentMailbox connects one mailbox on its OWN organizational domain.
//
// Distinct domains throughout, deliberately: the sender_domain dimension is derived
// from the address, so a fixture where several mailboxes share one email domain
// reports a second incident and every assertion about "the" incident becomes
// ambiguous. Each test that wants a sender_domain finding builds it explicitly.
func incidentMailbox(t *testing.T, f fixture, ws uuid.UUID, local, domain string) uuid.UUID {
	t.Helper()
	email := local + "@" + domain
	mb, err := f.q.CreateMailbox(f.ctx, gen.CreateMailboxParams{
		WorkspaceID: ws, Provider: "smtp", Email: email, DisplayName: local,
		SmtpHost: "smtp." + domain, SmtpPort: 587, SmtpUsername: email,
		ImapHost: "imap." + domain, ImapPort: 993, ImapUsername: email,
		SecretCiphertext: "ct", DailyCap: 50,
		MinIntervalSeconds: 0, RampEnabled: false, RampStartCap: 5, RampDays: 30,
	})
	if err != nil {
		t.Fatalf("mailbox %s: %v", email, err)
	}
	return mb.ID
}

// enrollParticipant enrolls a mailbox in the pool at an explicit position on both
// axes. Inserted directly rather than through UpsertParticipant because the axes are
// the evaluator's to write, and this file is about the READ.
func enrollParticipant(t *testing.T, f fixture, ws, mailbox uuid.UUID, health, lane string) {
	t.Helper()
	if _, err := f.pool.Exec(f.ctx,
		`INSERT INTO warmup_participants (mailbox_id, workspace_id, enabled, health_state, lane)
		 VALUES ($1, $2, true, $3, $4)`, mailbox, ws, health, lane); err != nil {
		t.Fatalf("enroll %s (%s/%s): %v", mailbox, health, lane, err)
	}
}

// seedDimensionObservation writes one placement observation carrying an explicit set
// of fault-dimension values, at an explicit age and trust.
//
// destination_esp, dkim_domain and return_path_domain are set independently so a test
// can leave one unresolved while the others carry a value — which is the case the
// per-column projection exists for.
func seedDimensionObservation(t *testing.T, f fixture, ws, mailbox uuid.UUID,
	key string, age time.Duration, destination, dkimDomain, returnPath string, trusted bool) {
	t.Helper()
	if _, err := f.pool.Exec(f.ctx,
		`INSERT INTO warmup_observations (workspace_id, mailbox_id, kind, placement, tab_capable,
		                                  destination_esp, dkim_domain, return_path_domain,
		                                  source, attribution_trusted, idempotency_key, observed_at)
		 VALUES ($1, $2, 'placement', 'inbox', false, $3, $4, $5,
		         'warmup_receipt', $6, $7, now() - $8::interval)`,
		ws, mailbox, destination, dkimDomain, returnPath, trusted, key, age.String()); err != nil {
		t.Fatalf("seed observation %s: %v", key, err)
	}
}

// oneIncidentParticipant reads the pool and returns the single participant a
// one-mailbox fixture must produce.
func oneIncidentParticipant(t *testing.T, f fixture, ws uuid.UUID) pwarmup.IncidentInput {
	t.Helper()
	rows, err := f.store.ListIncidentParticipants(f.ctx, ws)
	if err != nil {
		t.Fatalf("list incident participants: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d participants, want exactly 1: %+v", len(rows), rows)
	}
	return rows[0]
}

// Each dimension carries its OWN most-recent RESOLVED value, chosen independently.
//
// The fixture makes a single-row projection visibly wrong: the newest observation has
// no return path, the two newest have no destination, and an older row disagrees with
// all of it. Picking one row for all three columns would report the newest row's
// blanks as "unresolved" for dimensions we know perfectly well — a later poll that
// happened to extract less would erase a signing domain the engine has seen.
func TestIncidentParticipantsProjectEachDimensionsLatestResolvedValue(t *testing.T) {
	f := setup(t)
	mb := incidentMailbox(t, f, f.ws.ID, "solo", "solo.test")
	enrollParticipant(t, f, f.ws.ID, mb, pwarmup.StateWatch, pwarmup.LaneProbation)

	seedDimensionObservation(t, f, f.ws.ID, mb, "oldest", 5*time.Hour,
		"google", "old.test", "bounce.old.test", true)
	seedDimensionObservation(t, f, f.ws.ID, mb, "middle", 3*time.Hour,
		"unknown", "", "bounce.new.test", true)
	seedDimensionObservation(t, f, f.ws.ID, mb, "newest", time.Hour,
		"unknown", "new.test", "", true)

	got := oneIncidentParticipant(t, f, f.ws.ID)
	if got.SigningDomain != "new.test" {
		t.Errorf("signing domain = %q, want new.test (the newest row that carried one)", got.SigningDomain)
	}
	if got.ReturnPathDomain != "bounce.new.test" {
		t.Errorf("return path = %q, want bounce.new.test: the NEWEST row has none, so the newest row that "+
			"resolved this column wins — not the newest row overall", got.ReturnPathDomain)
	}
	if got.Route != "google" {
		t.Errorf("route = %q, want google: the two newer rows never resolved a destination, and `unknown` "+
			"is the absence of a classification rather than one", got.Route)
	}
	if got.MailboxID != mb.String() || got.Email != "solo@solo.test" {
		t.Errorf("participant identity = %s / %s, want %s / solo@solo.test", got.MailboxID, got.Email, mb)
	}
	if !got.Degraded {
		t.Error("a watch participant must read as degraded: watch is the first health band a shared cause moves")
	}
}

// The window and the attribution, which are the two ways this read could quietly
// correlate the wrong mail. Both are asserted on a mailbox whose ONLY rows are
// excluded ones, so the assertion cannot pass because a valid row happened to win.
func TestIncidentParticipantsIgnoreStaleAndUntrustedObservations(t *testing.T) {
	f := setup(t)
	mb := incidentMailbox(t, f, f.ws.ID, "excluded", "excluded.test")
	enrollParticipant(t, f, f.ws.ID, mb, pwarmup.StateHealthy, pwarmup.LaneHealthy)

	// Outside the trailing 7 days.
	seedDimensionObservation(t, f, f.ws.ID, mb, "stale", 8*24*time.Hour,
		"microsoft", "stale.test", "bounce.stale.test", true)
	// Inside the window, but the attribution was never established — the same gate
	// every placement rate in this subsystem uses, because anyone can send mail to a
	// connected mailbox claiming to be someone else.
	seedDimensionObservation(t, f, f.ws.ID, mb, "untrusted", time.Hour,
		"google", "forged.test", "bounce.forged.test", false)

	got := oneIncidentParticipant(t, f, f.ws.ID)
	if got.Route != "" || got.SigningDomain != "" || got.ReturnPathDomain != "" {
		t.Errorf("participant = %+v, want every dimension unresolved: an 8-day-old observation is outside "+
			"the window and an untrusted one is not evidence about this sender", got)
	}
	if got.Degraded {
		t.Error("a healthy participant must not read as degraded")
	}
}

// A participant with no observations at all still appears (design §9). It is in none
// of the three OBSERVED cohorts, but it is part of the pool the concentration is
// measured against — a clean mailbox outside the cohort is exactly what makes the
// inside look concentrated.
func TestIncidentParticipantsIncludeMailboxesWithNoObservations(t *testing.T) {
	f := setup(t)
	mb := incidentMailbox(t, f, f.ws.ID, "unobserved", "unobserved.test")
	enrollParticipant(t, f, f.ws.ID, mb, pwarmup.StateHealthy, pwarmup.LaneQuarantine)

	got := oneIncidentParticipant(t, f, f.ws.ID)
	if got.Route != "" || got.SigningDomain != "" || got.ReturnPathDomain != "" {
		t.Errorf("participant = %+v, want an empty value on every observed dimension", got)
	}
	if !got.Degraded {
		t.Error("a quarantined participant must read as degraded on the LANE axis alone: the two axes are " +
			"independent and a shared cause surfaces on either")
	}
}

// incidentCohort enrolls n mailboxes on their own domains, the first `degraded` of
// them quarantined, each carrying one observation with the given signing domain.
// Returns the degraded mailbox ids in the order created.
func incidentCohort(t *testing.T, f fixture, ws uuid.UUID, prefix, signingDomain string, n, degraded int) []string {
	t.Helper()
	members := []string{}
	for i := 1; i <= n; i++ {
		mb := incidentMailbox(t, f, ws, prefix, fmt.Sprintf("%s%d.test", prefix, i))
		health, lane := pwarmup.StateHealthy, pwarmup.LaneHealthy
		if i <= degraded {
			// The LANE axis alone, so the fixture proves both halves of the degradation
			// predicate are wired: a health-only fixture would pass with the lane arm
			// missing entirely.
			lane = pwarmup.LaneQuarantine
			members = append(members, mb.String())
		}
		enrollParticipant(t, f, ws, mb, health, lane)
		seedDimensionObservation(t, f, ws, mb, fmt.Sprintf("%s-%d", prefix, i), time.Hour,
			"unknown", signingDomain, "", true)
	}
	return members
}

// The end-to-end finding over real rows: three mailboxes sign as bad.test, two of them
// are contained, and the other three mailboxes in the pool are clean.
//
// The service is exercised rather than the store alone, because "the operator sees an
// attributed incident on the overview" is the behaviour, and the DTO is where the
// arithmetic becomes visible.
func TestOverviewReportsAnIncidentOverRealObservations(t *testing.T) {
	f := setup(t)
	ws := f.ws.ID
	members := incidentCohort(t, f, ws, "inside", "bad.test", 3, 2)
	incidentCohort(t, f, ws, "outside", "", 3, 0)

	ov, err := NewService(f.store).GetOverview(f.ctx, ws)
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	if len(ov.Incidents) != 1 {
		t.Fatalf("incidents = %+v, want exactly one (the shared signing domain)", ov.Incidents)
	}
	got := ov.Incidents[0]
	if got.Dimension != "signing_domain" || got.Value != "bad.test" {
		t.Fatalf("incident names %s=%q, want signing_domain=bad.test", got.Dimension, got.Value)
	}
	if got.CohortSize != 3 || got.DegradedInside != 2 || got.CohortOutside != 3 || got.DegradedOutside != 0 {
		t.Errorf("arithmetic = %+v, want cohort 3 / inside 2 / outside 3 / degraded outside 0", got)
	}
	// A spotless outside is the STRONGEST possible signal rather than a division by
	// zero: 67% inside against the continuity-corrected 17% outside is a lift of 4.
	if got.Lift != 4 {
		t.Errorf("lift = %v, want 4", got.Lift)
	}
	if len(got.MemberMailboxIDs) != 2 {
		t.Fatalf("members = %v, want the two contained mailboxes", got.MemberMailboxIDs)
	}
	for _, want := range members {
		found := false
		for _, member := range got.MemberMailboxIDs {
			found = found || member == want
		}
		if !found {
			t.Errorf("mailbox %s is degrading on bad.test but is not a member of %v", want, got.MemberMailboxIDs)
		}
	}
}

// THE tenancy boundary. Workspace B carries the SAME signing domain on the same
// number of mailboxes, all healthy, so a leak is visible in both directions: B's clean
// rows would dilute A's cohort (and its lift), and A's contained rows would manufacture
// an incident in B out of mailboxes nothing is wrong with.
func TestIncidentsNeverCrossAWorkspaceBoundary(t *testing.T) {
	f := setup(t)
	a := f.ws.ID
	incidentCohort(t, f, a, "inside", "shared.test", 3, 2)
	incidentCohort(t, f, a, "outside", "", 3, 0)

	other, err := f.q.CreateWorkspace(f.ctx, "Incident other "+uuid.NewString())
	if err != nil {
		t.Fatalf("other workspace: %v", err)
	}
	// Same value, same cohort size, nothing wrong with any of them.
	incidentCohort(t, f, other.ID, "peer", "shared.test", 3, 0)

	svc := NewService(f.store)
	ovA, err := svc.GetOverview(f.ctx, a)
	if err != nil {
		t.Fatalf("overview A: %v", err)
	}
	if len(ovA.Incidents) != 1 {
		t.Fatalf("workspace A incidents = %+v, want exactly one", ovA.Incidents)
	}
	if got := ovA.Incidents[0]; got.CohortSize != 3 || got.CohortOutside != 3 {
		t.Errorf("workspace A cohort = %d inside / %d outside, want 3 / 3: another tenant's mailboxes on "+
			"the same signing domain entered this workspace's arithmetic", got.CohortSize, got.CohortOutside)
	}
	for _, member := range ovA.Incidents[0].MemberMailboxIDs {
		if !mailboxInWorkspace(t, f, a, member) {
			t.Errorf("workspace A's incident names mailbox %s, which is not in workspace A", member)
		}
	}

	ovB, err := svc.GetOverview(f.ctx, other.ID)
	if err != nil {
		t.Fatalf("overview B: %v", err)
	}
	if len(ovB.Incidents) != 0 {
		t.Errorf("workspace B incidents = %+v, want none: its mailboxes are all healthy, so A's "+
			"degradations leaked across the tenancy boundary", ovB.Incidents)
	}
}

// mailboxInWorkspace answers the ownership question straight from Postgres, so the
// tenancy assertion above does not lean on the same read it is checking.
func mailboxInWorkspace(t *testing.T, f fixture, ws uuid.UUID, mailboxID string) bool {
	t.Helper()
	id, err := uuid.Parse(mailboxID)
	if err != nil {
		t.Fatalf("member id %q is not a uuid: %v", mailboxID, err)
	}
	var owned bool
	if err := f.pool.QueryRow(f.ctx,
		`SELECT EXISTS (SELECT 1 FROM mailboxes WHERE id = $1 AND workspace_id = $2)`, id, ws,
	).Scan(&owned); err != nil {
		t.Fatalf("ownership of %s: %v", mailboxID, err)
	}
	return owned
}

// A later observation that resolved NOTHING must not erase a fault domain the engine
// has seen. Same discipline as the overview's identity block (queries/warmup.sql): an
// identity is a STATE, and the last one observed stays the truth until a newer one
// CONTRADICTS it — a poll that extracted nothing contradicts nothing.
//
// The fixture is the state of a real pool mid-rollout: every mailbox's newest row
// carries the column defaults, and only the older rows name the signing domain. If
// unresolved values won, the shared cause would vanish on the next poll of any
// mailbox whose message did not parse.
//
// It also pins acceptance criterion 4 on real rows: all six mailboxes carry
// destination_esp 'unknown', two of them degrading, and that must NOT be reported as a
// route incident — a correlation on our own ignorance would fire hardest on the
// deployments with the least data.
func TestALaterUnresolvedObservationDoesNotEraseAKnownFaultDomain(t *testing.T) {
	f := setup(t)
	ws := f.ws.ID
	for i := 1; i <= 3; i++ {
		mb := incidentMailbox(t, f, ws, "faded", fmt.Sprintf("faded%d.test", i))
		lane := pwarmup.LaneHealthy
		if i <= 2 {
			lane = pwarmup.LaneQuarantine
		}
		enrollParticipant(t, f, ws, mb, pwarmup.StateHealthy, lane)
		seedDimensionObservation(t, f, ws, mb, fmt.Sprintf("faded-known-%d", i), 2*time.Hour,
			"unknown", "bad.test", "", true)
		// Newer, and carrying nothing at all: the column defaults, exactly as an
		// observation written by a poll that could not parse an identity looks.
		seedDimensionObservation(t, f, ws, mb, fmt.Sprintf("faded-blank-%d", i), time.Hour,
			"unknown", "", "", true)
	}
	incidentCohort(t, f, ws, "outside", "", 3, 0)

	ov, err := NewService(f.store).GetOverview(f.ctx, ws)
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	if len(ov.Incidents) != 1 {
		t.Fatalf("incidents = %+v, want exactly one: the shared signing domain is still known, and "+
			"`unknown` destinations must form nothing", ov.Incidents)
	}
	got := ov.Incidents[0]
	if got.Dimension != "signing_domain" || got.Value != "bad.test" || got.CohortSize != 3 {
		t.Errorf("incident = %+v, want the three-member bad.test signing cohort", got)
	}
}

// A disabled participant is frozen history, not a live signal — the same rule every
// other live read in queries/warmup.sql applies. Counting one would let a mailbox
// somebody removed from the pool months ago keep voting on today's correlation.
//
// The fixture isolates it: disabling the third member takes the cohort from 3 to 2,
// which is below MinIncidentCohort, so the incident must disappear entirely rather
// than merely change its arithmetic.
func TestDisabledParticipantsAreNotEvidence(t *testing.T) {
	f := setup(t)
	ws := f.ws.ID
	incidentCohort(t, f, ws, "inside", "bad.test", 3, 2)
	incidentCohort(t, f, ws, "outside", "", 3, 0)

	svc := NewService(f.store)
	before, err := svc.GetOverview(f.ctx, ws)
	if err != nil {
		t.Fatalf("overview before: %v", err)
	}
	if len(before.Incidents) != 1 {
		t.Fatalf("incidents = %+v, want one before anything is disabled", before.Incidents)
	}

	if _, err := f.pool.Exec(f.ctx,
		`UPDATE warmup_participants p SET enabled = false
		   FROM mailboxes m
		  WHERE m.id = p.mailbox_id AND p.workspace_id = $1 AND m.email = 'inside@inside3.test'`,
		ws); err != nil {
		t.Fatalf("disable participant: %v", err)
	}

	after, err := svc.GetOverview(f.ctx, ws)
	if err != nil {
		t.Fatalf("overview after: %v", err)
	}
	if len(after.Incidents) != 0 {
		t.Errorf("incidents = %+v, want none: the cohort is down to two live members, which is not a "+
			"pattern — it is those two mailboxes restated", after.Incidents)
	}
}

// The DESTINATION ROUTE dimension, driven end to end over real rows.
//
// Every other positive finding in this package correlates on signing_domain, because
// incidentCohort seeds `destination_esp = 'unknown'`. So the route dimension — the one
// security.md invariants 57 and 58 single out, because it is steerable inside a
// workspace and it is what an attacker would use to displace a true finding — was
// only ever proven in the pure fold. The SQL projection that feeds it real
// destination_esp values was not covered by any positive assertion.
//
// The fixture ISOLATES the route: signing domains and addresses are distinct per
// mailbox, so route is the only dimension that can correlate. The assertion checks
// for exactly one incident, which is what makes that isolation load-bearing rather
// than decorative.
func TestOverviewReportsARouteConcentratedIncident(t *testing.T) {
	f := setup(t)
	ws := f.ws.ID

	// Inside: three mailboxes whose warmup mail is delivered to Microsoft, two of
	// them contained on the LANE axis.
	members := []string{}
	for i := 1; i <= 3; i++ {
		mb := incidentMailbox(t, f, ws, "ms", fmt.Sprintf("ms%d.test", i))
		health, lane := pwarmup.StateHealthy, pwarmup.LaneHealthy
		if i <= 2 {
			lane = pwarmup.LaneQuarantine
			members = append(members, mb.String())
		}
		enrollParticipant(t, f, ws, mb, health, lane)
		seedDimensionObservation(t, f, ws, mb, fmt.Sprintf("ms-%d", i), time.Hour,
			"microsoft", fmt.Sprintf("sign-ms%d.test", i), "", true)
	}
	// Outside: three clean mailboxes delivered to Google.
	for i := 1; i <= 3; i++ {
		mb := incidentMailbox(t, f, ws, "goog", fmt.Sprintf("goog%d.test", i))
		enrollParticipant(t, f, ws, mb, pwarmup.StateHealthy, pwarmup.LaneHealthy)
		seedDimensionObservation(t, f, ws, mb, fmt.Sprintf("goog-%d", i), time.Hour,
			"google", fmt.Sprintf("sign-goog%d.test", i), "", true)
	}

	ov, err := NewService(f.store).GetOverview(f.ctx, ws)
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	if len(ov.Incidents) != 1 {
		t.Fatalf("incidents = %+v, want exactly one — the fixture isolates the route, so a "+
			"second finding means another dimension correlated and this no longer tests routes", ov.Incidents)
	}
	got := ov.Incidents[0]
	if got.Dimension != "destination_route" || got.Value != "microsoft" {
		t.Fatalf("incident names %s=%q, want destination_route=microsoft", got.Dimension, got.Value)
	}
	if got.CohortSize != 3 || got.DegradedInside != 2 || got.CohortOutside != 3 || got.DegradedOutside != 0 {
		t.Errorf("arithmetic = %+v, want cohort 3 / inside 2 / outside 3 / degraded outside 0", got)
	}
	// 67% inside against the continuity-corrected 17% outside.
	if got.Lift != 4 {
		t.Errorf("lift = %v, want 4", got.Lift)
	}
	if len(got.MemberMailboxIDs) != 2 {
		t.Fatalf("members = %v, want the two contained mailboxes", got.MemberMailboxIDs)
	}
	for _, want := range members {
		found := false
		for _, member := range got.MemberMailboxIDs {
			if member == want {
				found = true
			}
		}
		if !found {
			t.Errorf("member %s missing from %v", want, got.MemberMailboxIDs)
		}
	}
}
