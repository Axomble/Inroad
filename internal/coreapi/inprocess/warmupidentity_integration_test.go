//go:build integration

package inprocess

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/warmup"
)

// identityFacts is the five identity columns of one observation.
type identityFacts struct {
	dkimDomain, returnPathDomain string
	spf, dkim, dmarc             string
}

// readIdentity reads the identity columns off the placement observation attributed
// to the given SENDER. It asserts exactly one exists, because a second would mean
// the idempotency key stopped working and every count in this file would be a lie.
func readIdentity(t *testing.T, ctx context.Context, f warmupFixture, sender uuid.UUID) identityFacts {
	t.Helper()
	var got identityFacts
	rows, err := f.raw.Query(ctx,
		`SELECT dkim_domain, return_path_domain, spf_result, dkim_result, dmarc_result
		   FROM warmup_observations
		  WHERE workspace_id = $1 AND mailbox_id = $2 AND kind = 'placement'`,
		f.ws1, sender)
	if err != nil {
		t.Fatalf("read identity: %v", err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		n++
		if err := rows.Scan(&got.dkimDomain, &got.returnPathDomain, &got.spf, &got.dkim, &got.dmarc); err != nil {
			t.Fatalf("scan identity: %v", err)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate identity rows: %v", err)
	}
	if n != 1 {
		t.Fatalf("found %d placement observations for the sender, want exactly 1", n)
	}
	return got
}

// The identity travels with the receipt into the observation, and lands on the
// SENDER's row — not the recipient's. The recipient merely reported the verdicts;
// "how did our mail authenticate on arrival" is a fact about the mail A sent.
func TestReceiptIdentityLandsOnTheSenderAttributedObservation(t *testing.T) {
	ctx, f := setupWarmup(t)
	sendID, recipient := makeWarmupSend(t, ctx, f)

	if _, err := f.core.RecordWarmupReceipt(ctx, coreapi.WarmupReceiptInput{
		WorkspaceID: f.ws1.String(), WarmupSendID: sendID, RecipientMailbox: recipient,
		Placement: placementInbox, SourceFolder: "INBOX", MessageID: "<id@acme.test>",
		DKIMDomain: "acme.test", ReturnPathDomain: "bounce.acme.test",
		SPFResult: "pass", DKIMResult: "pass", DMARCResult: "fail",
	}); err != nil {
		t.Fatalf("RecordWarmupReceipt: %v", err)
	}

	got := readIdentity(t, ctx, f, f.a)
	want := identityFacts{"acme.test", "bounce.acme.test", "pass", "pass", "fail"}
	if got != want {
		t.Errorf("identity = %+v, want %+v", got, want)
	}

	// Attributed to A as the sender means B, who observed it, carries no placement
	// observation of its own — and therefore no identity that is not its own mail's.
	var observerRows int
	if err := f.raw.QueryRow(ctx,
		`SELECT count(*) FROM warmup_observations
		  WHERE workspace_id = $1 AND mailbox_id = $2 AND kind = 'placement'`,
		f.ws1, f.b).Scan(&observerRows); err != nil {
		t.Fatalf("count observer rows: %v", err)
	}
	if observerRows != 0 {
		t.Errorf("the recipient has %d placement observations, want 0: an identity attributed to "+
			"whoever READ the message would describe the wrong mailbox's sending posture", observerRows)
	}
}

// THE no-regression test for the empty case. A caller that knows nothing about
// identity sends five empty strings, and "" is not in the CHECK's vocabulary.
//
// Unhandled, that aborts the whole receipt transaction — the receipt, the placement
// and both daily stat writes — and the poller then returns before SetInboxCursor, so
// the message is re-fetched and re-fails identically on every pass. ALL inbound
// processing for the mailbox stops, campaign replies and bounce detection included.
// That is the tabbed-capability bug, and design §8 forbids repeating it for a field
// no decision reads.
func TestReceiptWithNoIdentityRecordsUnknownAndStillSucceeds(t *testing.T) {
	ctx, f := setupWarmup(t)
	sendID, recipient := makeWarmupSend(t, ctx, f)

	// Deliberately a ZERO-VALUED identity: no field set at all.
	plan, err := f.core.RecordWarmupReceipt(ctx, coreapi.WarmupReceiptInput{
		WorkspaceID: f.ws1.String(), WarmupSendID: sendID, RecipientMailbox: recipient,
		Placement: placementInbox, SourceFolder: "INBOX", MessageID: "<bare@acme.test>",
	})
	if err != nil {
		t.Fatalf("a receipt carrying no identity failed: %v — the CHECK rejected an empty verdict "+
			"and took the whole receipt transaction with it", err)
	}
	if plan.ReceiptID == "" {
		t.Fatal("no receipt recorded: the transaction rolled back")
	}

	got := readIdentity(t, ctx, f, f.a)
	want := identityFacts{"", "", "unknown", "unknown", "unknown"}
	if got != want {
		t.Errorf("identity = %+v, want %+v: an unsupplied verdict is unknown, never a pass and "+
			"never a fail", got, want)
	}

	// And the evidence the receipt exists FOR is intact.
	if _, inbox, spam, _ := todayStats(t, ctx, f, f.a); inbox != 1 || spam != 0 {
		t.Errorf("sender daily stats inbox=%d spam=%d, want 1/0", inbox, spam)
	}
}

// A verdict outside the vocabulary is coerced rather than refused, for the same
// reason: 'softfail' and 'temperror' are real RFC 8601 results this column does not
// carry, and a widened extractor emitting one must not be able to stop a mailbox's
// inbound pipeline. An over-long domain becomes empty rather than being truncated
// into a different domain.
func TestReceiptWithAnUnrecognisedVerdictIsCoercedNotRefused(t *testing.T) {
	ctx, f := setupWarmup(t)
	sendID, recipient := makeWarmupSend(t, ctx, f)

	overlong := ""
	for i := 0; i <= maxDomainLength; i++ {
		overlong += "a"
	}
	if _, err := f.core.RecordWarmupReceipt(ctx, coreapi.WarmupReceiptInput{
		WorkspaceID: f.ws1.String(), WarmupSendID: sendID, RecipientMailbox: recipient,
		Placement: placementInbox, SourceFolder: "INBOX", MessageID: "<odd@acme.test>",
		DKIMDomain: overlong, ReturnPathDomain: "bounce.acme.test",
		SPFResult: "softfail", DKIMResult: "temperror", DMARCResult: "PASS",
	}); err != nil {
		t.Fatalf("an unrecognised verdict aborted the receipt: %v", err)
	}

	got := readIdentity(t, ctx, f, f.a)
	want := identityFacts{"", "bounce.acme.test", "unknown", "unknown", "unknown"}
	if got != want {
		t.Errorf("identity = %+v, want %+v", got, want)
	}
}

// THE guard (design §9), mirroring slice A's. Two mailboxes, same workspace, same
// domain, the same amount of evidence observed at the same instant — one recorded
// with a full set of identity facts including dmarc=fail, the other with none at
// all. Both must come out of the evaluator with the SAME health state and the SAME
// lane.
//
// It asserts the PROMOTED outcome rather than merely equality, so it cannot pass by
// both mailboxes failing to qualify — which is how a guard like this quietly stops
// guarding.
//
// This is the test a later slice will break when it decides to "finish" the identity
// signal by wiring dmarc_result into a threshold. Breaking it must be a deliberate
// decision: the verdicts are permanently unknown for every provider that stamps
// none, so gating on them would penalise a whole provider class for our inability to
// observe it, and DNS-verifiable authentication posture is ALREADY gated, correctly
// and separately, by sending_domains and the pending_auth lane (design §7).
func TestRecordingIdentitiesChangesNoHealthStateAndNoLane(t *testing.T) {
	ctx, f := setupWarmup(t)
	f = withWallClock(t, f)
	seedAuthPassing(t, ctx, f, f.ws1, "acme.test")

	aToB := seedWarmupSendRow(t, ctx, f, f.a, f.b) // sender A, observed by B
	bToA := seedWarmupSendRow(t, ctx, f, f.b, f.a) // sender B, observed by A

	// Identical evidence: 25 trusted inbox placements each, same instant, same
	// reader capability. The ONLY difference is the identity metadata on B's rows.
	seedPlacementsFor(t, ctx, f, aToB, f.b, 25, placementInbox, false)
	seedPlacementsWithIdentity(t, ctx, f, bToA, f.a, 25, placementInbox,
		identityFacts{"acme.test", "bounce.acme.test", "fail", "fail", "fail"})

	if err := f.core.EvaluateWarmupHealth(ctx); err != nil {
		t.Fatalf("EvaluateWarmupHealth: %v", err)
	}

	bareHealth, bareLane := participantAxes(t, ctx, f, f.a)
	identHealth, identLane := participantAxes(t, ctx, f, f.b)
	if bareHealth != identHealth || bareLane != identLane {
		t.Fatalf("identical evidence decided differently: mailbox with no identity facts = %s/%s, "+
			"mailbox with spf/dkim/dmarc all FAIL = %s/%s. Recording how a message authenticated "+
			"must not move a health state or a lane — the verdicts are unobservable for a whole "+
			"provider class, and DNS-verifiable auth posture is already gated by sending_domains",
			bareHealth, bareLane, identHealth, identLane)
	}
	if identHealth != warmup.StateHealthy || identLane != warmup.LaneHealthy {
		t.Fatalf("the identity-recorded mailbox is %s/%s, want healthy/healthy: 25 qualified "+
			"placements must still qualify it, or this test could pass with both mailboxes unmeasured",
			identHealth, identLane)
	}
}

// seedPlacementsWithIdentity records n trusted sender-attributed placements that all
// carry the same identity facts. It writes through the same query the receipt path
// uses, so the CHECK and the column defaults are exercised, not bypassed.
func seedPlacementsWithIdentity(t *testing.T, ctx context.Context, f warmupFixture, sid, observer uuid.UUID, n int, placement string, id identityFacts) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := f.raw.Exec(ctx,
			`INSERT INTO warmup_observations (
			     workspace_id, mailbox_id, observer_mailbox_id, warmup_send_id,
			     kind, placement, tab_capable, source, attribution_trusted, idempotency_key,
			     dkim_domain, return_path_domain, spf_result, dkim_result, dmarc_result)
			 SELECT s.workspace_id, s.from_mailbox, $2, s.id, 'placement', $3::text, false,
			        'warmup_receipt', true, 'ident:' || $4::text,
			        $5::text, $6::text, $7::text, $8::text, $9::text
			   FROM warmup_sends s WHERE s.id = $1`,
			sid, observer, placement, uuid.NewString(),
			id.dkimDomain, id.returnPathDomain, id.spf, id.dkim, id.dmarc,
		); err != nil {
			t.Fatalf("seed identity placement %d: %v", i, err)
		}
	}
}

// Identity facts are read through a subquery the overview join adds, and this pins
// the observable property: one workspace's signing domain and auth verdicts never
// appear on another's overview row.
//
// What it does NOT prove, stated because the comment would otherwise imply it:
// deleting `WHERE o.workspace_id = $1` from that subquery does not make this fail.
// Verified by doing it. The subquery joins on `idf.mailbox_id = p.mailbox_id`, and
// a mailbox id is a globally unique UUID belonging to exactly one workspace, so
// ws2's participants cannot match ws1's observations however wide the subquery
// reads. The pin there is defense in depth; the join key is the actual barrier.
//
// It still earns its place. It catches the changes that WOULD leak — a join
// widened to a non-unique key (email, domain, provider), or an outer participant
// list that stopped being workspace-scoped — and those are exactly the refactors
// someone reaches for when adding the next slice's fault-domain rollups.
//
// The positive control matters as much as the negative one: without it, a query
// that returned nothing at all for ws1 would pass this test while being entirely
// broken.
func TestIdentityFactsDoNotCrossWorkspaces(t *testing.T) {
	ctx, f := setupWarmup(t)

	// ws2's mailbox has to be a participant, or the overview returns no rows for it
	// and the negative assertion is vacuous.
	if _, err := f.q.UpsertWarmupParticipant(ctx, gen.UpsertWarmupParticipantParams{
		MailboxID: f.c, WorkspaceID: f.ws2, StartVolume: 8, MaxVolume: 40, RampIncrement: 2,
	}); err != nil {
		t.Fatalf("make ws2 mailbox a participant: %v", err)
	}

	secret := identityFacts{
		dkimDomain: "signing.ws1-private.test", returnPathDomain: "bounce.ws1-private.test",
		spf: warmup.AuthFail, dkim: warmup.AuthFail, dmarc: warmup.AuthFail,
	}
	sid := seedWarmupSendRow(t, ctx, f, f.a, f.b)
	seedPlacementsWithIdentity(t, ctx, f, sid, f.b, 1, placementInbox, secret)

	foreign, err := f.q.ListWarmupOverviewRows(ctx, f.ws2)
	if err != nil {
		t.Fatalf("overview for ws2: %v", err)
	}
	if len(foreign) == 0 {
		t.Fatal("ws2 returned no overview rows, so this test would pass vacuously")
	}
	for _, r := range foreign {
		if r.IdentityDkimDomain != "" || r.IdentityReturnPathDomain != "" || r.IdentityObservedAt.Valid {
			t.Errorf("ws2 row %s carries ws1's identity: dkim=%q return_path=%q observed=%v",
				r.MailboxID, r.IdentityDkimDomain, r.IdentityReturnPathDomain, r.IdentityObservedAt.Valid)
		}
		// Unknown is the honest default for an unobserved mailbox; anything else
		// means a verdict leaked across the boundary.
		if r.IdentitySpfResult != warmup.AuthUnknown || r.IdentityDkimResult != warmup.AuthUnknown ||
			r.IdentityDmarcResult != warmup.AuthUnknown {
			t.Errorf("ws2 row %s carries ws1's verdicts: spf=%q dkim=%q dmarc=%q",
				r.MailboxID, r.IdentitySpfResult, r.IdentityDkimResult, r.IdentityDmarcResult)
		}
	}

	own, err := f.q.ListWarmupOverviewRows(ctx, f.ws1)
	if err != nil {
		t.Fatalf("overview for ws1: %v", err)
	}
	var sawOwn bool
	for _, r := range own {
		if r.MailboxID == f.a && r.IdentityDkimDomain == secret.dkimDomain {
			sawOwn = true
		}
	}
	if !sawOwn {
		t.Error("ws1 cannot see its OWN identity facts — the negative assertion above proves nothing")
	}
}
