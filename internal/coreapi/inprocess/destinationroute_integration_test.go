//go:build integration

package inprocess

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/warmup"
)

// setRecipientProvider rewrites a mailbox's transport tag. The ports move with it
// because 000057 requires an OAuth mailbox to carry none: a bare provider UPDATE
// violates that CHECK and the failure reads as a defect in the code under test.
func setRecipientProvider(t *testing.T, ctx context.Context, f warmupFixture, mailbox uuid.UUID, provider string) {
	t.Helper()
	if _, err := f.raw.Exec(ctx,
		`UPDATE mailboxes
		    SET provider  = $1,
		        smtp_port = CASE WHEN $1 = 'smtp' THEN 587 ELSE 0 END,
		        imap_port = CASE WHEN $1 = 'smtp' THEN 993 ELSE 0 END
		  WHERE id = $2 AND workspace_id = $3`,
		provider, mailbox, f.ws1); err != nil {
		t.Fatalf("set provider %q: %v", provider, err)
	}
}

// cacheDomainESP writes one COMPLETED MX classification into the workspace-scoped
// cache the recipientesp sweep owns — the only source the receipt path is allowed
// to consult for an smtp recipient.
func cacheDomainESP(t *testing.T, ctx context.Context, f warmupFixture, ws uuid.UUID, domain, classification string) {
	t.Helper()
	if err := f.q.UpsertRecipientDomain(ctx, gen.UpsertRecipientDomainParams{
		WorkspaceID: ws, Domain: domain, Esp: classification, MxHost: "mx." + domain,
	}); err != nil {
		t.Fatalf("cache %s=%s: %v", domain, classification, err)
	}
}

// readDestinations returns every placement observation's destination_esp for the
// given SENDER, oldest first, so a test can assert both the value and that a later
// receipt did not rewrite an earlier row.
func readDestinations(t *testing.T, ctx context.Context, f warmupFixture, sender uuid.UUID) []string {
	t.Helper()
	rows, err := f.raw.Query(ctx,
		`SELECT destination_esp FROM warmup_observations
		  WHERE workspace_id = $1 AND mailbox_id = $2 AND kind = 'placement'
		  ORDER BY observed_at, idempotency_key`,
		f.ws1, sender)
	if err != nil {
		t.Fatalf("read destinations: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			t.Fatalf("scan destination: %v", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate destinations: %v", err)
	}
	return out
}

// oneDestination is readDestinations for the single-observation case.
func oneDestination(t *testing.T, ctx context.Context, f warmupFixture, sender uuid.UUID) string {
	t.Helper()
	got := readDestinations(t, ctx, f, sender)
	if len(got) != 1 {
		t.Fatalf("found %d placement observations for the sender, want exactly 1: %v", len(got), got)
	}
	return got[0]
}

// recordReceipt drives one A→B send and its receipt, returning nothing: every test
// below asserts on the observation the receipt wrote rather than on its plan.
func recordReceipt(t *testing.T, ctx context.Context, f warmupFixture, messageID string) {
	t.Helper()
	sendID, recipient := makeWarmupSend(t, ctx, f)
	if _, err := f.core.RecordWarmupReceipt(ctx, coreapi.WarmupReceiptInput{
		WorkspaceID: f.ws1.String(), WarmupSendID: sendID, RecipientMailbox: recipient,
		Placement: placementInbox, SourceFolder: "INBOX", MessageID: messageID,
	}); err != nil {
		t.Fatalf("RecordWarmupReceipt(%s): %v", messageID, err)
	}
}

// The API providers are conclusive: the recipient mailbox IS hosted there, so the
// destination is settled without consulting DNS or the cache at all.
func TestReceiptResolvesTheDestinationFromTheRecipientProvider(t *testing.T) {
	for _, tc := range []struct{ provider, want string }{
		{"gmail", "google"},
		{"m365", "microsoft"},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			ctx, f := setupWarmup(t)
			setRecipientProvider(t, ctx, f, f.b, tc.provider)

			recordReceipt(t, ctx, f, "<provider@acme.test>")

			if got := oneDestination(t, ctx, f, f.a); got != tc.want {
				t.Errorf("destination_esp = %q, want %q for a %s recipient", got, tc.want, tc.provider)
			}
		})
	}
}

// For an smtp recipient only the MX cache knows who receives for its domain. This
// is the case the whole slice exists for: a self-hosted pool has no provider tag
// to read.
func TestReceiptResolvesAnSMTPDestinationFromTheMXCache(t *testing.T) {
	ctx, f := setupWarmup(t)
	// B is provider='smtp' out of the fixture, and its domain's MX is Microsoft's.
	cacheDomainESP(t, ctx, f, f.ws1, "acme.test", "microsoft")

	recordReceipt(t, ctx, f, "<cached@acme.test>")

	if got := oneDestination(t, ctx, f, f.a); got != "microsoft" {
		t.Errorf("destination_esp = %q, want \"microsoft\": for an smtp recipient the MX cache is the "+
			"only thing that knows where its mail is delivered", got)
	}
}

// A cache miss is `unknown`, and it must NOT be `other`: "we have not resolved this
// domain" and "we resolved it and it is neither Google nor Microsoft" are different
// facts, and a matrix that collapses them tells an operator they measured a route
// they never looked at.
//
// The receipt must also never resolve DNS to fill the gap. There is no resolver
// wired into this path to assert against, so what is asserted is the observable
// consequence: the miss stays a miss, and the cache is not populated as a side
// effect of the receipt.
func TestReceiptWithAnUnresolvedRecipientDomainRecordsUnknownAndDoesNotResolve(t *testing.T) {
	ctx, f := setupWarmup(t)

	recordReceipt(t, ctx, f, "<uncached@acme.test>")

	if got := oneDestination(t, ctx, f, f.a); got != "unknown" {
		t.Errorf("destination_esp = %q, want \"unknown\" for a domain the sweep has not classified", got)
	}
	var cached int
	if err := f.raw.QueryRow(ctx,
		`SELECT count(*) FROM recipient_domains WHERE workspace_id = $1 AND domain = 'acme.test'`,
		f.ws1).Scan(&cached); err != nil {
		t.Fatalf("count cache rows: %v", err)
	}
	if cached != 0 {
		t.Errorf("the receipt wrote %d recipient_domains rows: it resolved DNS on the receipt path, where "+
			"a slow resolver becomes a wedged poll cursor and stops ALL inbound processing", cached)
	}
}

// A row whose lookup never COMPLETED carries the 'unknown' default and no
// checked_at. Reading it as an answer would be indistinguishable from a real
// classification of "neither", which is the distinction recipient_domains exists to
// keep (its GetRecipientDomainESP predicate makes the same rule).
func TestReceiptIgnoresACacheRowWhoseLookupNeverCompleted(t *testing.T) {
	ctx, f := setupWarmup(t)
	if _, err := f.raw.Exec(ctx,
		`INSERT INTO recipient_domains (workspace_id, domain, esp) VALUES ($1, 'acme.test', 'other')`,
		f.ws1); err != nil {
		t.Fatalf("seed unchecked cache row: %v", err)
	}

	recordReceipt(t, ctx, f, "<unchecked@acme.test>")

	if got := oneDestination(t, ctx, f, f.a); got != "unknown" {
		t.Errorf("destination_esp = %q, want \"unknown\": a cache row with no checked_at is a lookup that "+
			"never completed, not an answer of \"neither\"", got)
	}
}

// The cache is workspace-scoped and the read must be pinned to the receipt's own
// workspace. Another tenant's classification of the same domain name is not
// evidence about this tenant's mail.
func TestReceiptDoesNotReadAnotherWorkspacesCachedDestination(t *testing.T) {
	ctx, f := setupWarmup(t)
	cacheDomainESP(t, ctx, f, f.ws2, "acme.test", "google")

	recordReceipt(t, ctx, f, "<foreign-cache@acme.test>")

	if got := oneDestination(t, ctx, f, f.a); got != "unknown" {
		t.Errorf("destination_esp = %q, want \"unknown\": workspace 2's cache row for the same domain "+
			"leaked into workspace 1's receipt", got)
	}
}

// THE immutability guard (design §5). A mailbox that migrates providers, or a
// domain whose MX changes, must not retroactively rewrite which route historical
// observations were measured on — a matrix that silently re-buckets its own history
// is worse than none.
//
// Both mutations are exercised in one pass because they reach the same value from
// the two different sources, and the fixture ISOLATES the guard: each receipt is
// asserted to have recorded a DIFFERENT route at the time it was written, so the
// test cannot pass by every row happening to agree.
func TestChangingTheRecipientProviderOrMXDoesNotRewriteHistoricalRoutes(t *testing.T) {
	ctx, f := setupWarmup(t)

	// 1. Delivered to Google, on the strength of the recipient's provider.
	setRecipientProvider(t, ctx, f, f.b, "gmail")
	recordReceipt(t, ctx, f, "<era-google@acme.test>")

	// 2. The mailbox migrates to a self-hosted server whose MX is Microsoft's. Mail
	//    sent from here on really does go somewhere else.
	setRecipientProvider(t, ctx, f, f.b, "smtp")
	cacheDomainESP(t, ctx, f, f.ws1, "acme.test", "microsoft")
	recordReceipt(t, ctx, f, "<era-microsoft@acme.test>")

	// 3. The domain's MX moves again, to a filter that is neither.
	cacheDomainESP(t, ctx, f, f.ws1, "acme.test", "other")
	recordReceipt(t, ctx, f, "<era-other@acme.test>")

	got := readDestinations(t, ctx, f, f.a)
	want := []string{"google", "microsoft", "other"}
	if len(got) != len(want) {
		t.Fatalf("found %d placement observations, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("observation %d reads %q, want %q — the matrix re-bucketed history when the "+
				"recipient's provider/MX changed, so a route it never measured now claims these samples",
				i, got[i], want[i])
		}
	}
}

// A re-poll that reclassifies a receipt to spam corrects the PLACEMENT and nothing
// else. The route was measured when the message arrived; the correction is about
// where it ended up in the recipient's mailbox, not about which system delivered it.
func TestReclassifyingToSpamKeepsTheRouteTheObservationWasMeasuredOn(t *testing.T) {
	ctx, f := setupWarmup(t)
	setRecipientProvider(t, ctx, f, f.b, "gmail")
	sendID, recipient := makeWarmupSend(t, ctx, f)

	in := coreapi.WarmupReceiptInput{
		WorkspaceID: f.ws1.String(), WarmupSendID: sendID, RecipientMailbox: recipient,
		Placement: placementInbox, SourceFolder: "INBOX", MessageID: "<moved@acme.test>",
	}
	if _, err := f.core.RecordWarmupReceipt(ctx, in); err != nil {
		t.Fatalf("first receipt: %v", err)
	}
	// The mailbox migrates before the message is found in junk, so a route re-derived
	// at correction time would come out different.
	setRecipientProvider(t, ctx, f, f.b, "smtp")
	in.Placement = placementSpam
	in.SourceFolder = "Junk"
	if _, err := f.core.RecordWarmupReceipt(ctx, in); err != nil {
		t.Fatalf("reclassifying receipt: %v", err)
	}

	var placement, destination string
	if err := f.raw.QueryRow(ctx,
		`SELECT placement, destination_esp FROM warmup_observations
		  WHERE workspace_id = $1 AND mailbox_id = $2 AND kind = 'placement'`,
		f.ws1, f.a).Scan(&placement, &destination); err != nil {
		t.Fatalf("read observation: %v", err)
	}
	if placement != placementSpam {
		t.Errorf("placement = %q, want spam: the reclassification did not land", placement)
	}
	if destination != "google" {
		t.Errorf("destination_esp = %q, want \"google\": correcting where a message ENDED UP must not "+
			"re-derive which system DELIVERED it", destination)
	}
}

// THE guard on "this gates nothing" (design §7). Two mailboxes, same workspace, same
// domain, same amount of evidence, observed at the same instant — one recorded
// against a resolved route, the other against `unknown`. Both must come out of the
// evaluator with the SAME health state and the SAME lane.
//
// Asserted against the PROMOTED outcome rather than merely "equal", so it cannot
// pass by both mailboxes failing to qualify: if splitting by destination leaked into
// the placement denominator, the routed mailbox would drop under MinPlacementSamples
// and read as `unknown` — demoted by a recording that observed nothing new.
//
// The reason this must not gate is NOT slice A's and B's. A tab and an
// Authentication-Results verdict are structurally unobservable on a whole provider
// class, so gating them would penalise that class forever. A per-route rate is fully
// observable wherever the route exists; what is missing is CALIBRATION — nobody has
// yet seen what a normal Google→Microsoft warmup spam rate looks like in this
// system, so a threshold set today would be a guess dressed as a policy. That
// condition expires when real matrices exist, which is the point of shipping this
// before slice E. Breaking this test is therefore a deliberate decision to be made
// against data, not a bug to be fixed.
func TestRecordingDestinationRoutesChangesNoHealthStateAndNoLane(t *testing.T) {
	ctx, f := setupWarmup(t)
	f = withWallClock(t, f)
	seedAuthPassing(t, ctx, f, f.ws1, "acme.test")

	aToB := seedWarmupSendRow(t, ctx, f, f.a, f.b) // sender A, observed by B
	bToA := seedWarmupSendRow(t, ctx, f, f.b, f.a) // sender B, observed by A
	seedRoutedPlacements(t, ctx, f, aToB, f.b, 25, placementInbox, "unknown")
	seedRoutedPlacements(t, ctx, f, bToA, f.a, 25, placementInbox, "microsoft")

	if err := f.core.EvaluateWarmupHealth(ctx); err != nil {
		t.Fatalf("EvaluateWarmupHealth: %v", err)
	}

	unroutedHealth, unroutedLane := participantAxes(t, ctx, f, f.a)
	routedHealth, routedLane := participantAxes(t, ctx, f, f.b)
	if unroutedHealth != routedHealth || unroutedLane != routedLane {
		t.Fatalf("identical evidence decided differently: unrouted mailbox = %s/%s, routed mailbox = %s/%s. "+
			"Recording WHERE a message was delivered must not change a health state or a lane — no "+
			"calibration data exists to set a per-route threshold against (design §7)",
			unroutedHealth, unroutedLane, routedHealth, routedLane)
	}
	if routedHealth != warmup.StateHealthy || routedLane != warmup.LaneHealthy {
		t.Fatalf("the routed mailbox is %s/%s, want healthy/healthy: 25 qualified placements must still "+
			"qualify it, or this test could pass with both mailboxes unmeasured", routedHealth, routedLane)
	}
}

// seedRoutedPlacements records n trusted placement observations attributed to the
// sender of sid, all observed now, against an explicit destination route.
func seedRoutedPlacements(t *testing.T, ctx context.Context, f warmupFixture, sid, observer uuid.UUID,
	n int, placement, destination string) {
	t.Helper()
	observed := pgtype.Timestamptz{Time: time.Now(), Valid: true}
	for i := 0; i < n; i++ {
		if err := f.q.RecordWarmupPlacementObservation(ctx, gen.RecordWarmupPlacementObservationParams{
			WorkspaceID: f.ws1, WarmupSendID: sid, RecipientMailbox: observer,
			ReceiptID: uuid.New(), Placement: placement, ObservedAt: observed,
			DestinationEsp: destination,
		}); err != nil {
			t.Fatalf("seed %s placement %d to %s: %v", placement, i, destination, err)
		}
	}
}
