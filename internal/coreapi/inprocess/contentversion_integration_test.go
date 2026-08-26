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

// sendContentVersion reads what a send recorded about the content it carried.
func sendContentVersion(t *testing.T, ctx context.Context, f warmupFixture, sendID uuid.UUID) string {
	t.Helper()
	var v string
	if err := f.raw.QueryRow(ctx,
		`SELECT content_version FROM warmup_sends WHERE id = $1 AND workspace_id = $2`,
		sendID, f.ws1).Scan(&v); err != nil {
		t.Fatalf("read send content_version: %v", err)
	}
	return v
}

// observationContentVersions returns the content version of every placement
// observation attributed to the given SENDER, oldest first.
func observationContentVersions(t *testing.T, ctx context.Context, f warmupFixture, sender uuid.UUID) []string {
	t.Helper()
	rows, err := f.raw.Query(ctx,
		`SELECT content_version FROM warmup_observations
		  WHERE workspace_id = $1 AND mailbox_id = $2 AND kind = 'placement'
		  ORDER BY observed_at, idempotency_key`,
		f.ws1, sender)
	if err != nil {
		t.Fatalf("read observation content_versions: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scan content_version: %v", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate content_versions: %v", err)
	}
	return out
}

// threadContentKey reads the seed a send's thread was opened with, so a test can
// recompute the version the send SHOULD have recorded rather than restating a digest.
func threadContentKey(t *testing.T, ctx context.Context, f warmupFixture, sendID uuid.UUID) (string, int32) {
	t.Helper()
	var key string
	var turn int32
	if err := f.raw.QueryRow(ctx,
		`SELECT th.content_key, th.turn FROM warmup_threads th
		   JOIN warmup_sends s ON s.thread_id = th.id AND s.workspace_id = th.workspace_id
		  WHERE s.id = $1 AND s.workspace_id = $2`,
		sendID, f.ws1).Scan(&key, &turn); err != nil {
		t.Fatalf("read thread content key: %v", err)
	}
	return key, turn
}

// The send is the only place that knows which library template produced a message,
// and it knows it before delivery. If the version were not written at claim time the
// receipt would have nothing to copy, and the placement could never be attributed.
func TestASendRecordsTheContentVersionItCarried(t *testing.T) {
	ctx, f := setupWarmup(t)

	sendIDStr, _ := makeWarmupSend(t, ctx, f)
	sendID, err := uuid.Parse(sendIDStr)
	if err != nil {
		t.Fatalf("parse send id: %v", err)
	}

	got := sendContentVersion(t, ctx, f, sendID)
	if got == "" {
		t.Fatal("the send recorded no content version, so its placement can never be attributed to " +
			"the template that produced it")
	}
	// Recomputed from the thread's persisted seed rather than pinned to a literal, so
	// this test asserts the WIRING and leaves the derivation to the pure tests.
	key, _ := threadContentKey(t, ctx, f, sendID)
	if want := warmup.ContentVersion(key, 0); got != want {
		t.Errorf("send recorded %q, want %q — the version does not identify the content the send "+
			"actually resolved from its own content_key", got, want)
	}
}

// The observation is what the aggregation groups, so the value has to reach it. It is
// copied off the send inside RecordWarmupPlacementObservation's own INSERT, not passed
// in by the poller — the poller has no idea which template produced the message it read.
func TestThePlacementObservationCarriesTheSendsContentVersion(t *testing.T) {
	ctx, f := setupWarmup(t)

	sendIDStr, recipient := makeWarmupSend(t, ctx, f)
	sendID, err := uuid.Parse(sendIDStr)
	if err != nil {
		t.Fatalf("parse send id: %v", err)
	}
	if _, err := f.core.RecordWarmupReceipt(ctx, coreapi.WarmupReceiptInput{
		WorkspaceID: f.ws1.String(), WarmupSendID: sendIDStr, RecipientMailbox: recipient,
		Placement: placementInbox, SourceFolder: "INBOX", MessageID: "<attributed@acme.test>",
	}); err != nil {
		t.Fatalf("RecordWarmupReceipt: %v", err)
	}

	want := sendContentVersion(t, ctx, f, sendID)
	if want == "" {
		t.Fatal("the send recorded no content version; this test cannot prove the carry")
	}
	got := observationContentVersions(t, ctx, f, f.a)
	if len(got) != 1 {
		t.Fatalf("found %d placement observations, want 1: %v", len(got), got)
	}
	if got[0] != want {
		t.Errorf("observation carries %q but the send recorded %q — the grouping key and the send "+
			"disagree, so placement is attributed to content that was not sent", got[0], want)
	}
}

// An engagement reply sends a DIFFERENT body from the opener, under the same thread.
// Attributing both to one version would blend an opener that gets filtered with a
// reply that does not — the two land differently for reasons unrelated to wording.
func TestAReplySendRecordsItsOwnTurnsContentVersion(t *testing.T) {
	ctx, f := setupWarmup(t)

	openerID := seedWarmupSendRow(t, ctx, f, f.a, f.b)
	key, turn := threadContentKey(t, ctx, f, openerID)
	if turn != 0 {
		t.Fatalf("seeded thread is at turn %d, want 0", turn)
	}

	opener := warmup.ContentVersion(key, 0)
	reply := warmup.ContentVersion(key, 1)
	if opener == "" || reply == "" {
		t.Fatalf("the seeded thread does not have two turns (opener=%q reply=%q)", opener, reply)
	}
	if opener == reply {
		t.Errorf("turn 0 and turn 1 of thread %q share the version %q — a reply's placement would be "+
			"attributed to the opener's body", key, opener)
	}
}

// seedContentVersionPlacements records n trusted placements attributed to the sender
// of sid, all observed now, for a send stamped with an explicit content version.
//
// The version is stamped on the SEND and copied by the observation write, which is the
// path production uses; setting it on the observation directly would test a route
// nothing takes.
func seedContentVersionPlacements(t *testing.T, ctx context.Context, f warmupFixture,
	ws, sid, observer uuid.UUID, version string, inbox, spam int) {
	t.Helper()
	if _, err := f.raw.Exec(ctx,
		`UPDATE warmup_sends SET content_version = $1 WHERE id = $2 AND workspace_id = $3`,
		version, sid, ws); err != nil {
		t.Fatalf("stamp content version %q: %v", version, err)
	}
	observed := pgtype.Timestamptz{Time: time.Now(), Valid: true}
	record := func(placement string, n int) {
		for i := 0; i < n; i++ {
			if err := f.q.RecordWarmupPlacementObservation(ctx, gen.RecordWarmupPlacementObservationParams{
				WorkspaceID: ws, WarmupSendID: sid, RecipientMailbox: observer,
				ReceiptID: uuid.New(), Placement: placement, ObservedAt: observed,
			}); err != nil {
				t.Fatalf("seed %s placement %d for %q: %v", placement, i, version, err)
			}
		}
	}
	record(placementInbox, inbox)
	record(placementSpam, spam)
}

// contentVersionRows folds the workspace aggregation into a lookup keyed by version.
func contentVersionRows(t *testing.T, ctx context.Context, f warmupFixture,
	ws uuid.UUID) map[string]warmup.ContentVersionPlacement {
	t.Helper()
	rows, err := f.q.ListWarmupContentVersions(ctx, ws)
	if err != nil {
		t.Fatalf("ListWarmupContentVersions: %v", err)
	}
	stats := make([]warmup.ContentVersionStat, len(rows))
	for i, r := range rows {
		stats[i] = warmup.ContentVersionStat{
			Version: r.ContentVersion, Inbox: int(r.Inbox7d), Spam: int(r.Spam7d),
		}
	}
	out := map[string]warmup.ContentVersionPlacement{}
	for _, p := range warmup.FoldContentVersions(stats) {
		out[p.Version] = p
	}
	return out
}

// THE per-denominator test, against real SQL.
//
// The two versions are seeded so a POOLED computation is visibly wrong rather than
// merely differently rounded: 30 clean sends of version A and 30 all-spam sends of
// version B. Per version that is 0% and 100% spam. Pooled it is 50% for both, which
// would report the clean template as half-failing and the failing one as half-fine.
//
// Both versions are attributed to the SAME sender, so the split cannot be an artifact
// of two mailboxes having different records — the only thing that differs is the content.
func TestTheAggregationGivesEachContentVersionItsOwnDenominator(t *testing.T) {
	ctx, f := setupWarmup(t)
	const clean = "sl1:1111111111111111"
	const dirty = "sl1:2222222222222222"

	seedContentVersionPlacements(t, ctx, f, f.ws1,
		seedWarmupSendRow(t, ctx, f, f.a, f.b), f.b, clean, 30, 0)
	seedContentVersionPlacements(t, ctx, f, f.ws1,
		seedWarmupSendRow(t, ctx, f, f.a, f.b), f.b, dirty, 0, 30)

	got := contentVersionRows(t, ctx, f, f.ws1)
	if len(got) != 2 {
		t.Fatalf("aggregation returned %d versions, want 2: %v", len(got), got)
	}
	for version, wantSample := range map[string]int{clean: 30, dirty: 30} {
		if p, ok := got[version]; !ok {
			t.Fatalf("version %q missing from the aggregation", version)
		} else if p.PlacementSample != wantSample {
			t.Errorf("version %q has sample %d, want %d — the two versions' observations were pooled",
				version, p.PlacementSample, wantSample)
		}
	}
	if p := got[clean]; p.SpamRate == nil || *p.SpamRate != 0 {
		t.Errorf("clean version's spam rate = %v, want 0; pooled it would be 0.5", p.SpamRate)
	}
	if p := got[dirty]; p.SpamRate == nil || *p.SpamRate != 1 {
		t.Errorf("spam-only version's spam rate = %v, want 1; pooled it would be 0.5", p.SpamRate)
	}
}

// Splitting a shared library by (template, turn) makes small cells the normal case, so
// this is the branch most versions take. Below the floor the counts are reported and
// the rate is withheld — a 1-of-2 spam observation is not a 50% spam rate.
//
// The established version beside it is what stops this passing vacuously: if the floor
// were applied to the POOLED total, both would qualify and neither would read as
// not-established.
func TestAContentVersionBelowTheSampleFloorReportsNoRate(t *testing.T) {
	ctx, f := setupWarmup(t)
	const sparse = "sl1:3333333333333333"
	const measured = "sl1:4444444444444444"
	const sparseSpam = 1
	sparseInbox := warmup.MinPlacementSamples - 2 // one short of the floor, with the spam

	seedContentVersionPlacements(t, ctx, f, f.ws1,
		seedWarmupSendRow(t, ctx, f, f.a, f.b), f.b, sparse, sparseInbox, sparseSpam)
	seedContentVersionPlacements(t, ctx, f, f.ws1,
		seedWarmupSendRow(t, ctx, f, f.a, f.b), f.b, measured, warmup.MinPlacementSamples, 0)

	got := contentVersionRows(t, ctx, f, f.ws1)
	sparseRow, ok := got[sparse]
	if !ok {
		t.Fatalf("version %q missing from the aggregation: %v", sparse, got)
	}
	if sparseRow.PlacementSample != sparseInbox+sparseSpam {
		t.Fatalf("sparse version sample = %d, want %d",
			sparseRow.PlacementSample, sparseInbox+sparseSpam)
	}
	if sparseRow.SpamRate != nil || sparseRow.InboxRate != nil {
		t.Errorf("a %d-sample version reported rates (inbox=%v spam=%v); the floor is %d, and pooling "+
			"the other version's %d observations into this denominator is what would clear it",
			sparseRow.PlacementSample, sparseRow.InboxRate, sparseRow.SpamRate,
			warmup.MinPlacementSamples, warmup.MinPlacementSamples)
	}
	if sparseRow.Spam != sparseSpam || sparseRow.Inbox != sparseInbox {
		t.Errorf("sparse version counts = %d inbox / %d spam, want %d / %d: not-established withholds "+
			"the RATE, not the evidence", sparseRow.Inbox, sparseRow.Spam, sparseInbox, sparseSpam)
	}
	if measuredRow := got[measured]; measuredRow.SpamRate == nil {
		t.Errorf("the version with exactly %d samples reported no rate either, so this test could "+
			"pass with the floor applied to nothing at all", warmup.MinPlacementSamples)
	}
}

// Tenancy. The content library is shared across the codebase, so two workspaces
// legitimately send the SAME version — which makes this the axis on which a missing
// workspace pin would look like real data rather than an obvious leak.
func TestTheAggregationIsPinnedToItsOwnWorkspace(t *testing.T) {
	ctx, f := setupWarmup(t)
	const shared = "sl1:5555555555555555"

	seedContentVersionPlacements(t, ctx, f, f.ws1,
		seedWarmupSendRow(t, ctx, f, f.a, f.b), f.b, shared, 5, 0)
	// The other workspace sends the same content, and a lot more of it. It needs a
	// second mailbox of its own: a warmup thread's two ends must differ
	// (warmup_threads_distinct_mailboxes_check), and the fixture gives ws2 only one.
	foreignPartner := itObserverMailbox(t, ctx, f, f.ws2, "d@other.test")
	foreign := seedWarmupSendRowIn(t, ctx, f, f.ws2, f.c, foreignPartner)
	seedContentVersionPlacements(t, ctx, f, f.ws2, foreign, foreignPartner, shared, 40, 10)

	got := contentVersionRows(t, ctx, f, f.ws1)
	row, ok := got[shared]
	if !ok {
		t.Fatalf("version %q missing from workspace 1's aggregation: %v", shared, got)
	}
	if row.PlacementSample != 5 {
		t.Fatalf("workspace 1 reports %d samples for %q, want 5 — workspace 2's 50 observations of the "+
			"same content leaked in", row.PlacementSample, shared)
	}
	if row.SpamRate != nil {
		t.Errorf("workspace 1's %d-sample version reported a rate; only workspace 2's volume could "+
			"have cleared the floor", row.PlacementSample)
	}
}

// A re-poll that finds the message in junk corrects WHERE it landed. It must not
// change WHICH content produced it — that was settled when the message was sent. The
// same immutability rule slice C's route follows, and it matters more here: the
// reclassify path supersedes the observation in place, so a version rewritten at
// correction time would move a spam observation into a different template's bucket.
func TestReclassifyingToSpamKeepsTheContentVersionTheObservationWasRecordedWith(t *testing.T) {
	ctx, f := setupWarmup(t)

	sendIDStr, recipient := makeWarmupSend(t, ctx, f)
	sendID, err := uuid.Parse(sendIDStr)
	if err != nil {
		t.Fatalf("parse send id: %v", err)
	}
	want := sendContentVersion(t, ctx, f, sendID)
	if want == "" {
		t.Fatal("the send recorded no content version; this test cannot prove anything is preserved")
	}

	in := coreapi.WarmupReceiptInput{
		WorkspaceID: f.ws1.String(), WarmupSendID: sendIDStr, RecipientMailbox: recipient,
		Placement: placementInbox, SourceFolder: "INBOX", MessageID: "<moved-content@acme.test>",
	}
	if _, err := f.core.RecordWarmupReceipt(ctx, in); err != nil {
		t.Fatalf("first receipt: %v", err)
	}
	// The library could be edited between the send and the correction; stamping a
	// different version on the send proves the correction reads the OBSERVATION's
	// recorded value and does not re-derive one.
	if _, err := f.raw.Exec(ctx,
		`UPDATE warmup_sends SET content_version = 'sl1:6666666666666666' WHERE id = $1 AND workspace_id = $2`,
		sendID, f.ws1); err != nil {
		t.Fatalf("restamp send: %v", err)
	}
	in.Placement = placementSpam
	in.SourceFolder = "Junk"
	if _, err := f.core.RecordWarmupReceipt(ctx, in); err != nil {
		t.Fatalf("reclassifying receipt: %v", err)
	}

	var placement, version string
	if err := f.raw.QueryRow(ctx,
		`SELECT placement, content_version FROM warmup_observations
		  WHERE workspace_id = $1 AND mailbox_id = $2 AND kind = 'placement'`,
		f.ws1, f.a).Scan(&placement, &version); err != nil {
		t.Fatalf("read observation: %v", err)
	}
	if placement != placementSpam {
		t.Errorf("placement = %q, want spam: the reclassification did not land", placement)
	}
	if version != want {
		t.Errorf("content_version = %q, want %q: correcting where a message ENDED UP re-attributed it "+
			"to different content", version, want)
	}
}

// The column's CHECK is a shape guard, and this is what it is guarding against: the
// read side GROUPs BY this column, so an unstable value does not fail loudly — it
// becomes a version of its own and shatters one template into a wall of one-sample
// rows nobody can read. Refusing it in the DATABASE is what makes the guard
// unskippable by a future writer.
func TestPostgresRefusesAContentVersionThatCouldNotBeStable(t *testing.T) {
	ctx, f := setupWarmup(t)
	sid := seedWarmupSendRow(t, ctx, f, f.a, f.b)

	for _, unstable := range []string{
		uuid.New().String(),               // a per-send id
		"2026-08-21T10:00:00Z",            // a timestamp
		"7",                               // a library index
		"sl1:ABCDEF0123456789",            // upper-case: two spellings of one digest
		"sl1:deadbeef",                    // short digest
		"Hi, do you have the Q3 figures?", // an expanded body
	} {
		if _, err := f.raw.Exec(ctx,
			`UPDATE warmup_sends SET content_version = $1 WHERE id = $2 AND workspace_id = $3`,
			unstable, sid, f.ws1); err == nil {
			t.Errorf("Postgres accepted content_version %q on warmup_sends; a value that varies per "+
				"send makes every rate in the report unreadable", unstable)
		}
	}
	// And the value the library actually produces must pass, or the send would abort.
	produced := warmup.ContentVersion("some-content-key", 0)
	if _, err := f.raw.Exec(ctx,
		`UPDATE warmup_sends SET content_version = $1 WHERE id = $2 AND workspace_id = $3`,
		produced, sid, f.ws1); err != nil {
		t.Fatalf("Postgres rejected the library's own version %q: %v", produced, err)
	}
}

// THE guard on "this gates nothing". Two mailboxes, same workspace, same domain, same
// amount of evidence, observed at the same instant — one whose sends recorded a content
// version and one whose did not. Both must come out of the evaluator with the SAME
// health state and the SAME lane.
//
// Asserted against the PROMOTED outcome rather than merely "equal", so it cannot pass
// by both mailboxes failing to qualify: if attributing content leaked into the
// placement denominator, the attributed mailbox would drop under MinPlacementSamples
// and read as unknown — demoted by a recording that observed nothing new.
//
// The reason this must not gate is NOT slice A's or B's (a tab and an
// Authentication-Results verdict are structurally unobservable on a whole provider
// class, so gating them would penalise that class forever). Two calibration problems
// stack instead: the per-version sample is necessarily small, because one library is
// shared across the pool and then split by turn; and a template's apparent spam rate
// is confounded with whichever mailboxes happened to draw it, because content is not
// assigned experimentally. Breaking this test is therefore a deliberate decision to be
// made against data and a controlled assignment, not a bug to be fixed.
func TestRecordingContentVersionsChangesNoHealthStateAndNoLane(t *testing.T) {
	ctx, f := setupWarmup(t)
	f = withWallClock(t, f)
	seedAuthPassing(t, ctx, f, f.ws1, "acme.test")

	aToB := seedWarmupSendRow(t, ctx, f, f.a, f.b) // sender A, observed by B
	bToA := seedWarmupSendRow(t, ctx, f, f.b, f.a) // sender B, observed by A
	// A's sends carry no attribution (the pre-slice state); B's carry a version.
	seedContentVersionPlacements(t, ctx, f, f.ws1, aToB, f.b, "", 25, 0)
	seedContentVersionPlacements(t, ctx, f, f.ws1, bToA, f.a, "sl1:7777777777777777", 25, 0)

	if err := f.core.EvaluateWarmupHealth(ctx); err != nil {
		t.Fatalf("EvaluateWarmupHealth: %v", err)
	}

	unattributedHealth, unattributedLane := participantAxes(t, ctx, f, f.a)
	attributedHealth, attributedLane := participantAxes(t, ctx, f, f.b)
	if unattributedHealth != attributedHealth || unattributedLane != attributedLane {
		t.Fatalf("identical evidence decided differently: unattributed mailbox = %s/%s, attributed "+
			"mailbox = %s/%s. Recording WHICH CONTENT produced a placement must not change a health "+
			"state or a lane — the per-version sample is small by construction and the rate is "+
			"confounded with the senders that drew the template",
			unattributedHealth, unattributedLane, attributedHealth, attributedLane)
	}
	if attributedHealth != warmup.StateHealthy || attributedLane != warmup.LaneHealthy {
		t.Fatalf("the attributed mailbox is %s/%s, want healthy/healthy: 25 qualified placements must "+
			"still qualify it, or this test could pass with both mailboxes unmeasured",
			attributedHealth, attributedLane)
	}
}
