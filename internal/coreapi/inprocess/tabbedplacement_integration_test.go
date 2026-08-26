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

// A Gmail Promotions landing is recorded as `tabbed` on BOTH tables — the receipt
// the poller writes and the observation the policy reads, in ONE transaction, so a
// value one table accepted and the other rejected would abort the whole receipt.
//
// Everything else about it stays an inbox landing, which is the point of the
// vocabulary rather than an accident of it: the sender's daily inbox counter moves,
// and the recipient is not asked to rescue a message that was never in spam.
func TestTabbedPlacementIsRecordedOnBothTablesWithItsCapability(t *testing.T) {
	ctx, f := setupWarmup(t)
	sendID, recipient := makeWarmupSend(t, ctx, f)

	plan, err := f.core.RecordWarmupReceipt(ctx, coreapi.WarmupReceiptInput{
		WorkspaceID: f.ws1.String(), WarmupSendID: sendID, RecipientMailbox: recipient,
		Placement: placementTabbed, SourceFolder: "INBOX", MessageID: "<tab@acme.test>",
		TabCapable: true,
	})
	if err != nil {
		t.Fatalf("RecordWarmupReceipt(tabbed): %v", err)
	}
	if plan.ReceiptID == "" {
		t.Fatal("no receipt recorded for a tabbed placement")
	}
	if plan.DoRescue {
		t.Error("DoRescue = true for a tabbed placement: the message is in the inbox, not in spam")
	}

	var receiptPlacement string
	if err := f.raw.QueryRow(ctx,
		`SELECT placement FROM warmup_receipts WHERE workspace_id = $1 AND warmup_send_id = $2`,
		f.ws1, sendID).Scan(&receiptPlacement); err != nil {
		t.Fatalf("read receipt: %v", err)
	}
	if receiptPlacement != placementTabbed {
		t.Errorf("receipt placement = %q, want %q", receiptPlacement, placementTabbed)
	}

	var observationPlacement string
	var tabCapable bool
	if err := f.raw.QueryRow(ctx,
		`SELECT placement, tab_capable FROM warmup_observations
		  WHERE workspace_id = $1 AND mailbox_id = $2 AND kind = 'placement'`,
		f.ws1, f.a).Scan(&observationPlacement, &tabCapable); err != nil {
		t.Fatalf("read observation: %v", err)
	}
	if observationPlacement != placementTabbed {
		t.Errorf("observation placement = %q, want %q", observationPlacement, placementTabbed)
	}
	if !tabCapable {
		t.Error("tab_capable = false on an observation whose reader named the tab")
	}

	// The daily projection the overview and the deliverability score read counts it
	// on the INBOX side, exactly as it did when a Promotions landing was recorded as
	// `inbox`. Anything else would make this vocabulary change move numbers nobody
	// asked it to move.
	if _, inbox, spam, _ := todayStats(t, ctx, f, f.a); inbox != 1 || spam != 0 {
		t.Errorf("sender daily stats inbox=%d spam=%d, want 1/0: a tabbed message still landed in the inbox", inbox, spam)
	}
}

// The IMAP counterpart, and the honest half of the asymmetry: an inbox landing
// observed over IMAP records `inbox` with tab_capable FALSE. IMAP has no concept of
// a tab, so claiming the capability would put this row in a denominator it cannot
// inform.
func TestIMAPPlacementIsRecordedAsNotTabCapable(t *testing.T) {
	ctx, f := setupWarmup(t)
	sendID, recipient := makeWarmupSend(t, ctx, f)

	if _, err := f.core.RecordWarmupReceipt(ctx, coreapi.WarmupReceiptInput{
		WorkspaceID: f.ws1.String(), WarmupSendID: sendID, RecipientMailbox: recipient,
		Placement: placementInbox, SourceFolder: "INBOX", TabCapable: false,
	}); err != nil {
		t.Fatalf("RecordWarmupReceipt(inbox): %v", err)
	}

	var placement string
	var tabCapable bool
	if err := f.raw.QueryRow(ctx,
		`SELECT placement, tab_capable FROM warmup_observations
		  WHERE workspace_id = $1 AND mailbox_id = $2 AND kind = 'placement'`,
		f.ws1, f.a).Scan(&placement, &tabCapable); err != nil {
		t.Fatalf("read observation: %v", err)
	}
	if placement != placementInbox {
		t.Errorf("placement = %q, want inbox", placement)
	}
	if tabCapable {
		t.Error("tab_capable = true for an IMAP observation, where a tab is not observable at all")
	}
}

// THE denominator test. A tabbed rate over ALL placement observations would
// under-report for exactly the reason the bounce denominators were wrong: an IMAP
// mailbox cannot contribute a tabbed observation, so pooling its rows dilutes the
// numerator toward zero and the rate reads clean for a pool that is mostly
// untested.
//
// The fixture makes the two answers far apart on purpose: 5 tabbed of 15 tab-capable
// observations is 33%, while the same 5 over all 35 placements is 14% — under any
// band a later slice might set.
func TestTabbedDenominatorCountsOnlyTabCapableObservations(t *testing.T) {
	ctx, f := setupWarmup(t)
	sid := seedWarmupSendRow(t, ctx, f, f.a, f.b)

	seedPlacementsWithCapability(t, ctx, f, sid, 10, placementInbox, true)  // Gmail, primary
	seedPlacementsWithCapability(t, ctx, f, sid, 5, placementTabbed, true)  // Gmail, a tab
	seedPlacementsWithCapability(t, ctx, f, sid, 20, placementInbox, false) // IMAP: cannot report a tab

	if _, err := f.q.UpsertWarmupSignalSnapshotsForWorkspace(ctx, f.ws1); err != nil {
		t.Fatalf("refresh snapshots: %v", err)
	}

	var inbox, tabbed, capable int
	if err := f.raw.QueryRow(ctx,
		`SELECT placement_inbox, placement_tabbed, placement_tab_capable
		   FROM warmup_signal_snapshots WHERE workspace_id = $1 AND mailbox_id = $2`,
		f.ws1, f.a).Scan(&inbox, &tabbed, &capable); err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if capable == 35 {
		t.Fatal("the tabbed denominator pooled the IMAP observations: 20 rows that structurally cannot " +
			"report a tab diluted the rate from 33% to 14%")
	}
	if capable != 15 {
		t.Errorf("placement_tab_capable = %d, want 15 (the Gmail-observed rows only)", capable)
	}
	if tabbed != 5 {
		t.Errorf("placement_tabbed = %d, want 5", tabbed)
	}
	// And the existing inbox-side count is unchanged by the vocabulary: all 35
	// messages landed in the inbox, 5 of them in a tab.
	if inbox != 35 {
		t.Errorf("placement_inbox = %d, want 35 (a tabbed message still landed in the inbox — "+
			"dropping it here would push the mailbox under MinPlacementSamples)", inbox)
	}
}

// THE guard on "this gates nothing". Two mailboxes, same workspace, same domain,
// same amount of evidence, observed at the same instant — one recorded `inbox`, the
// other `tabbed`. Both must come out of the evaluator with the SAME health state and
// the SAME lane.
//
// It is asserted against the PROMOTED outcome rather than merely "equal", so it
// cannot pass by both mailboxes failing to qualify: if `tabbed` fell out of the
// placement denominator, the tabbed mailbox would drop under MinPlacementSamples
// and read as `unknown` — demoted by a vocabulary change that observed nothing new.
//
// This is the test most likely to be quietly broken by a later slice that decides to
// "finish" the tabbed signal by wiring it into a threshold. Breaking it is exactly
// the decision that needs to be made deliberately: a tab is undetectable on an
// entire provider class, so gating on it would make promotion unreachable for every
// SMTP mailbox, or demand assuming primary placement where we cannot see.
func TestWideningThePlacementVocabularyChangesNoHealthStateAndNoLane(t *testing.T) {
	ctx, f := setupWarmup(t)
	f = withWallClock(t, f)
	seedAuthPassing(t, ctx, f, f.ws1, "acme.test")

	aToB := seedWarmupSendRow(t, ctx, f, f.a, f.b) // sender A, observed by B
	bToA := seedWarmupSendRow(t, ctx, f, f.b, f.a) // sender B, observed by A
	seedPlacementsFor(t, ctx, f, aToB, f.b, 25, placementInbox, false)
	seedPlacementsFor(t, ctx, f, bToA, f.a, 25, placementTabbed, true)

	if err := f.core.EvaluateWarmupHealth(ctx); err != nil {
		t.Fatalf("EvaluateWarmupHealth: %v", err)
	}

	inboxHealth, inboxLane := participantAxes(t, ctx, f, f.a)
	tabbedHealth, tabbedLane := participantAxes(t, ctx, f, f.b)
	if inboxHealth != tabbedHealth || inboxLane != tabbedLane {
		t.Fatalf("identical evidence decided differently: inbox-recorded mailbox = %s/%s, "+
			"tabbed-recorded mailbox = %s/%s. Recording WHICH tab a message landed in must not "+
			"change a health state or a lane — the signal is undetectable on a whole provider class",
			inboxHealth, inboxLane, tabbedHealth, tabbedLane)
	}
	if tabbedHealth != warmup.StateHealthy || tabbedLane != warmup.LaneHealthy {
		t.Fatalf("the tabbed-recorded mailbox is %s/%s, want healthy/healthy: 25 qualified placements "+
			"must still qualify it, or this test could pass with both mailboxes unmeasured",
			tabbedHealth, tabbedLane)
	}
}

// seedPlacementsWithCapability records n trusted placement observations attributed
// to A as the SENDER, all observed now, with an explicit placement and reader
// capability.
func seedPlacementsWithCapability(t *testing.T, ctx context.Context, f warmupFixture, sid uuid.UUID, n int, placement string, tabCapable bool) {
	t.Helper()
	seedPlacementsFor(t, ctx, f, sid, f.b, n, placement, tabCapable)
}

// seedPlacementsFor is the same for an explicit send + observing mailbox, so a test
// can attribute evidence to either mailbox of the pair. The observation writer
// resolves the sender from the send itself, which is what makes the direction
// matter.
func seedPlacementsFor(t *testing.T, ctx context.Context, f warmupFixture, sid, observer uuid.UUID, n int, placement string, tabCapable bool) {
	t.Helper()
	observed := pgtype.Timestamptz{Time: time.Now(), Valid: true}
	for i := 0; i < n; i++ {
		if err := f.q.RecordWarmupPlacementObservation(ctx, gen.RecordWarmupPlacementObservationParams{
			WorkspaceID: f.ws1, WarmupSendID: sid, RecipientMailbox: observer,
			ReceiptID: uuid.New(), Placement: placement, TabCapable: tabCapable,
			ObservedAt: observed,
		}); err != nil {
			t.Fatalf("seed %s placement %d: %v", placement, i, err)
		}
	}
}
