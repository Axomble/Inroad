package inbox

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/replyclassify"
)

// These tests lock in the HEADER-LOSS fallback (Microsoft strips unknown custom
// headers, so warmup mail to/from an M365 mailbox arrives with no
// X-Inroad-Warmup token at all) and — more importantly — the isolation invariant
// the fallback must not weaken: widening detection must not let CAMPAIGN mail
// become a warmup receipt, and must not give a FORGED token a second door.

// warmupLookupCore adds the OPTIONAL header-loss lookup to warmupStubCore.
//
// Kept as its OWN type rather than a warmupStubCore method — mirroring
// fakeInboxCapture — so a test can drive a core that does NOT implement the
// capability (a plain *warmupStubCore) and prove the poller degrades to
// header-only detection instead of failing the poll.
type warmupLookupCore struct {
	*warmupStubCore
	// sends is keyed on the EXACT (workspace, to-mailbox, message-id) triple the
	// poller must pin, so a lookup that widens any one of the three misses here
	// exactly as it would miss the real query's WHERE clause.
	sends map[warmupLookupKey]string
	// lookups records every triple asked for, so a test can prove a lookup was
	// NOT attempted at all (the forged-token case) as well as what it asked.
	lookups []warmupLookupKey
	err     error
}

type warmupLookupKey struct {
	workspaceID, toMailboxID, messageID string
}

// FindWarmupSendByMessageID keys on the raw value it is handed. Angle-bracket
// normalisation is the STORE's job (internal/coreapi/inprocess, where the
// comparison lives and is tested), so this fake deliberately does not normalise:
// a test here asserts what the poller passes, never what the store would match.
func (w *warmupLookupCore) FindWarmupSendByMessageID(_ context.Context, workspaceID, toMailboxID, messageID string) (coreapi.WarmupSendRef, bool, error) {
	k := warmupLookupKey{workspaceID, toMailboxID, messageID}
	w.lookups = append(w.lookups, k)
	if w.err != nil {
		return coreapi.WarmupSendRef{}, false, w.err
	}
	if id, ok := w.sends[k]; ok {
		return coreapi.WarmupSendRef{WarmupSendID: id}, true, nil
	}
	return coreapi.WarmupSendRef{}, false, nil
}

// Compile-time proof the fake still satisfies the capability. The neighbouring
// WarmupEvidenceClient assertion exists because a signature change once made the
// type assertion fail at RUNTIME and the poller silently fell through to campaign
// classification — the same failure mode applies here.
var _ coreapi.WarmupSendLookupClient = (*warmupLookupCore)(nil)

func newWarmupLookupCore(t *testing.T) *warmupLookupCore {
	t.Helper()
	return &warmupLookupCore{
		warmupStubCore: newWarmupCore(t),
		sends:          map[warmupLookupKey]string{},
	}
}

// seedWarmupSend registers a sent warmup send addressed to (workspace, mailbox)
// with the given Message-ID, as warmup_sends.message_id records it.
func (w *warmupLookupCore) seedWarmupSend(workspaceID, toMailboxID, messageID, sendID string) {
	w.sends[warmupLookupKey{workspaceID, toMailboxID, messageID}] = sendID
}

// headerlessMsg is a message with NO X-Inroad-Warmup header — what an M365
// mailbox delivers for warmup mail, and also what every ordinary campaign reply
// looks like. Its In-Reply-To matches the seeded send in newWarmupCore, so the
// isolation tests prove the fallback decides between the two on the Message-ID
// alone.
func headerlessMsg(t *testing.T, uid uint32, messageID, inReplyTo string) mail.InboundMessage {
	t.Helper()
	raw := "From: partner@warm.test\nTo: me@mb.test\nSubject: Re: catching up\n" +
		"Message-ID: " + messageID + "\nIn-Reply-To: " + inReplyTo +
		"\n\nGreat, talk soon.\n"
	return inboundMsg(t, uid, raw)
}

// THE isolation test (warmup spec §9.4). An ordinary campaign reply has no
// warmup header either, so the fallback is asked about EVERY headerless message.
// One whose Message-ID matches no warmup send must classify exactly as it did
// before: a reply that stops its enrollment, not a warmup receipt.
func TestPollCampaignReplyWithNoWarmupSendMatchStillClassifiesAsAReply(t *testing.T) {
	core := newWarmupLookupCore(t)
	enq := &spyEngageEnqueuer{}
	reader := &fakeReader{
		uidValidity: 5, uidNext: 12,
		msgs: []mail.InboundMessage{headerlessMsg(t, 11, "<campaign-reply@corp>", "<root@x>")},
	}

	if err := runWarmupPoll(t, core, reader, enq); err != nil {
		t.Fatal(err)
	}
	if len(core.receipts) != 0 {
		t.Fatalf("a campaign reply must NEVER become a warmup receipt, got %d", len(core.receipts))
	}
	if len(enq.calls) != 0 {
		t.Fatalf("a campaign reply must not enqueue warmup engagement, got %+v", enq.calls)
	}
	if len(core.replied) != 1 || core.replied[0] != "e1" {
		t.Fatalf("the reply must still stop its enrollment (MarkReplied e1), got %v", core.replied)
	}
}

// The bug this item fixes: Microsoft strips the X-Inroad-Warmup header, so a
// warmup message delivered to an M365 mailbox arrives with no token and used to
// fall through into campaign classification — recording no receipt, so placement
// (and the health state machine it feeds) undercounted for every M365 mailbox.
func TestPollHeaderlessWarmupIsRecoveredByMessageID(t *testing.T) {
	core := newWarmupLookupCore(t)
	core.seedWarmupSend(pollWS, pollMailbox, "<wm-headerless@warm>", "send-hl")
	enq := &spyEngageEnqueuer{}
	reader := &fakeReader{
		uidValidity: 5, uidNext: 12,
		msgs: []mail.InboundMessage{headerlessMsg(t, 11, "<wm-headerless@warm>", "<root@x>")},
	}

	if err := runWarmupPoll(t, core, reader, enq); err != nil {
		t.Fatal(err)
	}
	if len(core.receipts) != 1 {
		t.Fatalf("expected the header-loss fallback to record 1 receipt, got %d", len(core.receipts))
	}
	got := core.receipts[0]
	if got.WarmupSendID != "send-hl" {
		t.Errorf("receipt WarmupSendID = %q, want send-hl (the id from the warmup_sends row)", got.WarmupSendID)
	}
	if got.WorkspaceID != pollWS || got.RecipientMailbox != pollMailbox {
		t.Errorf("receipt ws/recipient = %q/%q, want %q/%q", got.WorkspaceID, got.RecipientMailbox, pollWS, pollMailbox)
	}
	if got.Placement != placementInbox || got.SourceFolder != sourceFolderInbox {
		t.Errorf("receipt placement/folder = %q/%q, want inbox/INBOX", got.Placement, got.SourceFolder)
	}
	if len(enq.calls) != 1 || enq.calls[0].receiptID != "rcpt-1" {
		t.Errorf("a recovered receipt must be engaged like any other, got %+v", enq.calls)
	}
	// Recovered warmup mail is still warmup mail: isolation applies unchanged,
	// even though its In-Reply-To matches a seeded campaign send.
	if len(core.replied) != 0 || len(core.recorded) != 0 || len(core.unsubscribed) != 0 {
		t.Fatalf("recovered warmup mail must NOT be classified; replied=%v recorded=%v unsub=%v",
			core.replied, core.recorded, core.unsubscribed)
	}
}

// A present-but-forged token is an attack signal, not a header-loss case. Letting
// it retry through the Message-ID path would hand an attacker a second door: the
// forged token names a send id we would refuse, so a fallback would let them
// substitute a REAL one they merely have to guess the Message-ID of. Only a
// genuinely ABSENT header may fall back — asserted by the lookup never happening.
func TestPollForgedWarmupTokenNeverFallsBackToMessageID(t *testing.T) {
	core := newWarmupLookupCore(t)
	core.seedWarmupSend(pollWS, pollMailbox, "<forged@warm>", "send-forged")
	enq := &spyEngageEnqueuer{}
	forged := warmupToken(t, []byte("attacker-secret"), pollWS, "send-x")
	reader := &fakeReader{
		uidValidity: 5, uidNext: 12,
		msgs: []mail.InboundMessage{warmupMsg(t, 11, forged, "<forged@warm>", "<root@x>")},
	}

	if err := runWarmupPoll(t, core, reader, enq); err != nil {
		t.Fatal(err)
	}
	if len(core.lookups) != 0 {
		t.Fatalf("a forged token must not even ATTEMPT the Message-ID fallback, got %+v", core.lookups)
	}
	if len(core.receipts) != 0 {
		t.Fatalf("a forged token must not record a receipt, got %d", len(core.receipts))
	}
	if len(core.tokenFailures) != 1 || !strings.HasPrefix(core.tokenFailures[0], "invalid_signature:") {
		t.Fatalf("the token failure must still be recorded, got %v", core.tokenFailures)
	}
	if len(core.replied) != 1 {
		t.Fatalf("a forged-token message is classified normally, got replied=%v", core.replied)
	}
}

// Same rule for a correctly-signed token naming ANOTHER workspace: the header was
// present and it did not verify for this tenant, so there is nothing to recover.
func TestPollWrongWorkspaceWarmupTokenNeverFallsBackToMessageID(t *testing.T) {
	core := newWarmupLookupCore(t)
	core.seedWarmupSend(pollWS, pollMailbox, "<xws@warm>", "send-xws")
	enq := &spyEngageEnqueuer{}
	crossWS := warmupToken(t, warmupSecret, "another-workspace", "send-y")
	reader := &fakeReader{
		uidValidity: 5, uidNext: 12,
		msgs: []mail.InboundMessage{warmupMsg(t, 11, crossWS, "<xws@warm>", "<root@x>")},
	}

	if err := runWarmupPoll(t, core, reader, enq); err != nil {
		t.Fatal(err)
	}
	if len(core.lookups) != 0 {
		t.Fatalf("a wrong-workspace token must not attempt the fallback, got %+v", core.lookups)
	}
	if len(core.receipts) != 0 {
		t.Fatalf("a wrong-workspace token must not record a receipt, got %d", len(core.receipts))
	}
	if len(core.tokenFailures) != 1 || !strings.HasPrefix(core.tokenFailures[0], "workspace_mismatch:") {
		t.Fatalf("the token failure must still be recorded, got %v", core.tokenFailures)
	}
}

// The lookup is scoped to the polled mailbox in the polled workspace, both taken
// from the task payload. A warmup send with the same Message-ID addressed to a
// DIFFERENT mailbox, or belonging to a DIFFERENT workspace, is not this mailbox's
// receipt: the fake keys on the exact triple, so a widened pin misses.
func TestPollHeaderlessWarmupLookupIsPinnedToTheWorkspaceAndPolledMailbox(t *testing.T) {
	cases := []struct {
		name                    string
		seedWorkspace, seedMbox string
	}{
		{"another workspace", "another-workspace", pollMailbox},
		{"another mailbox in this workspace", pollWS, "mb-other"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			core := newWarmupLookupCore(t)
			core.seedWarmupSend(tc.seedWorkspace, tc.seedMbox, "<wm-pin@warm>", "send-pin")
			reader := &fakeReader{
				uidValidity: 5, uidNext: 12,
				msgs: []mail.InboundMessage{headerlessMsg(t, 11, "<wm-pin@warm>", "<none@x>")},
			}

			if err := runWarmupPoll(t, core, reader, &spyEngageEnqueuer{}); err != nil {
				t.Fatal(err)
			}
			if len(core.receipts) != 0 {
				t.Fatalf("a send from %s/%s must not match this poll, got %d receipts",
					tc.seedWorkspace, tc.seedMbox, len(core.receipts))
			}
			want := warmupLookupKey{pollWS, pollMailbox, "<wm-pin@warm>"}
			if len(core.lookups) != 1 || core.lookups[0] != want {
				t.Fatalf("lookups = %+v, want exactly one %+v (workspace + polled mailbox from the task)",
					core.lookups, want)
			}
		})
	}
}

// A message with no Message-ID at all cannot be matched to anything, and the
// column's own default is the empty string — a lookup on "" would match every
// unsent row. No id, no lookup.
func TestPollMessageWithNoMessageIDAttemptsNoFallbackLookup(t *testing.T) {
	core := newWarmupLookupCore(t)
	raw := "From: partner@warm.test\nTo: me@mb.test\nSubject: no id here\n\nbody\n"
	reader := &fakeReader{
		uidValidity: 5, uidNext: 12,
		msgs: []mail.InboundMessage{inboundMsg(t, 11, raw)},
	}

	if err := runWarmupPoll(t, core, reader, &spyEngageEnqueuer{}); err != nil {
		t.Fatal(err)
	}
	if len(core.lookups) != 0 {
		t.Fatalf("a message with no Message-ID must not be looked up, got %+v", core.lookups)
	}
}

// A core WITHOUT the capability (a future HTTP coreapi that has not grown the
// endpoint, or any of the existing worker fakes) must degrade to header-only
// detection: no fallback, and above all no failed poll.
func TestPollWithoutTheLookupCapabilityDegradesToHeaderOnlyDetection(t *testing.T) {
	core := newWarmupCore(t) // no FindWarmupSendByMessageID
	reader := &fakeReader{
		uidValidity: 5, uidNext: 12,
		msgs: []mail.InboundMessage{headerlessMsg(t, 11, "<wm-nocap@warm>", "<none@x>")},
	}

	if err := runWarmupPoll(t, core, reader, &spyEngageEnqueuer{}); err != nil {
		t.Fatalf("a core without the lookup capability must not fail the poll, got %v", err)
	}
	if len(core.receipts) != 0 {
		t.Fatalf("no capability means no fallback, got %d receipts", len(core.receipts))
	}
	if !core.cursorSet {
		t.Fatal("the poll must still complete and advance the cursor")
	}
}

// A lookup FAILURE on the INBOX path is not a verdict: it means we do not know
// whether this message is warmup. The poll fails so the cursor stays put and the
// retry re-examines it, matching the neighbouring RecordWarmupReceipt failure
// policy — the alternative is silently classifying a possible warmup message as
// campaign mail and never revisiting it.
func TestPollHeaderlessWarmupLookupErrorFailsTheInboxPoll(t *testing.T) {
	core := newWarmupLookupCore(t)
	core.err = errors.New("db down")
	reader := &fakeReader{
		uidValidity: 5, uidNext: 12,
		msgs: []mail.InboundMessage{headerlessMsg(t, 11, "<wm-err@warm>", "<root@x>")},
	}

	if err := runWarmupPoll(t, core, reader, &spyEngageEnqueuer{}); !errors.Is(err, core.err) {
		t.Fatalf("expected the lookup error to fail the poll, got %v", err)
	}
	if core.cursorSet {
		t.Fatal("a failed fallback lookup must not advance the INBOX cursor")
	}
	if len(core.replied) != 0 {
		t.Fatalf("an unresolved message must not be classified as a reply, got %v", core.replied)
	}
}

// The junk scan is where header loss costs the most: spam placement is the core
// deliverability signal warmup exists to measure, and an M365 mailbox's junk-foldered
// warmup mail carries no token either.
func TestPollJunkHeaderlessWarmupIsRecoveredAsSpam(t *testing.T) {
	core := newWarmupLookupCore(t)
	core.seedWarmupSend(pollWS, pollMailbox, "<wm-junk-hl@warm>", "send-junk-hl")
	enq := &spyEngageEnqueuer{}
	reader := &fakeReader{
		uidValidity: 5, uidNext: 12,
		junkFolder: "Junk",
		junkMsgs:   []mail.InboundMessage{headerlessMsg(t, 0, "<wm-junk-hl@warm>", "<none@x>")},
	}

	if err := runWarmupPoll(t, core, reader, enq); err != nil {
		t.Fatal(err)
	}
	if len(core.receipts) != 1 {
		t.Fatalf("expected 1 recovered spam receipt, got %d", len(core.receipts))
	}
	got := core.receipts[0]
	if got.Placement != placementSpam || got.SourceFolder != "Junk" {
		t.Errorf("receipt placement/folder = %q/%q, want spam/Junk", got.Placement, got.SourceFolder)
	}
	if got.WarmupSendID != "send-junk-hl" {
		t.Errorf("receipt WarmupSendID = %q, want send-junk-hl", got.WarmupSendID)
	}
}

// The junk scan's no-fail policy covers the fallback too: the scan is stateless and
// idempotent, so a lookup failure there is logged and the next poll rescans, rather
// than holding back the INBOX cursor that every other inbound signal depends on.
func TestPollJunkFallbackLookupErrorDoesNotFailThePoll(t *testing.T) {
	core := newWarmupLookupCore(t)
	core.err = errors.New("db down")
	reader := &fakeReader{
		uidValidity: 5, uidNext: 12,
		junkFolder: "Junk",
		junkMsgs:   []mail.InboundMessage{headerlessMsg(t, 0, "<wm-junk-err@warm>", "<none@x>")},
	}

	if err := runWarmupPoll(t, core, reader, &spyEngageEnqueuer{}); err != nil {
		t.Fatalf("a junk-path lookup failure must not fail the poll, got %v", err)
	}
	if !core.cursorSet {
		t.Fatal("the INBOX cursor must still advance when a junk lookup errors")
	}
	if len(core.receipts) != 0 {
		t.Fatalf("a failed lookup records nothing, got %d receipts", len(core.receipts))
	}
}

// A verified token still wins outright: the fallback is a recovery path, not a
// second opinion, so a message that HAS a valid token is never looked up.
func TestPollVerifiedWarmupTokenSkipsTheFallbackLookup(t *testing.T) {
	core := newWarmupLookupCore(t)
	token := warmupToken(t, warmupSecret, pollWS, "send-tok")
	reader := &fakeReader{
		uidValidity: 5, uidNext: 12,
		msgs: []mail.InboundMessage{warmupMsg(t, 11, token, "<wm-tok@warm>", "<none@x>")},
	}

	if err := runWarmupPoll(t, core, reader, &spyEngageEnqueuer{}); err != nil {
		t.Fatal(err)
	}
	if len(core.lookups) != 0 {
		t.Fatalf("a verified token needs no lookup, got %+v", core.lookups)
	}
	if len(core.receipts) != 1 || core.receipts[0].WarmupSendID != "send-tok" {
		t.Fatalf("the receipt must come from the token payload, got %+v", core.receipts)
	}
}

// The m365 branch end-to-end, because it is the provider the bug is about: a
// Graph-delivered warmup message with no header is recovered and recorded.
func TestPollM365HeaderlessWarmupIsRecovered(t *testing.T) {
	core := &warmupLookupCore{
		warmupStubCore: &warmupStubCore{
			stubCore: &stubCore{job: coreapi.InboxPollJob{Provider: "m365", AccessToken: []byte("tok"), Cursor: "delta-old"}},
			plan:     coreapi.WarmupEngagePlan{ReceiptID: "rcpt-m365-hl", EngageAfter: time.Minute},
		},
		sends: map[warmupLookupKey]string{},
	}
	core.seedWarmupSend(pollWS, pollMailbox, "<wm-m365-hl@warm>", "send-m365-hl")
	graph := &fakeJunkGraph{inboxMsgs: []mail.InboundMessage{headerlessMsg(t, 0, "<wm-m365-hl@warm>", "<none@x>")}}

	err := PollHandler(core, nil, nil, graph, replyclassify.New(nil), warmupSecret, &spyEngageEnqueuer{})(context.Background(), pollTask(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(core.receipts) != 1 || core.receipts[0].WarmupSendID != "send-m365-hl" {
		t.Fatalf("expected the m365 header-loss recovery to record send-m365-hl, got %+v", core.receipts)
	}
	if core.receipts[0].TabCapable {
		t.Error("TabCapable = true for a Graph reader, which reports relevance rather than tabs")
	}
}
