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
	"github.com/inroad/inroad/internal/platform/warmup"
)

// These tests lock in the warmup receipt-detection HOOK and the campaign-
// classification ISOLATION invariant (spec §9.4) without a live IMAP server or a
// DB: a fake reader supplies the fetched message, a warmupStubCore records the
// RecordWarmupReceipt call and returns a plan, and a spy enqueuer captures the
// warmup:engage follow-up. Detection is verified BEFORE trust — a forged or
// wrong-workspace token falls through to normal classification.

const (
	pollWS      = "ws1" // matches pollTask's WorkspaceID
	pollMailbox = "mb1" // matches pollTask's MailboxID (the receipt recipient)
)

var warmupSecret = []byte("warmup-signing-secret")

// warmupStubCore adds RecordWarmupReceipt to the shared stubCore so the same fake
// serves both the classification path (embedded methods) and the warmup path.
type warmupStubCore struct {
	*stubCore
	receipts            []coreapi.WarmupReceiptInput
	plan                coreapi.WarmupEngagePlan
	recordErr           error
	tokenFailures       []string
	hardBounceObservers []string
	hardBounceIDs       []string
	hardBounceMatched   bool
}

func (w *warmupStubCore) RecordWarmupReceipt(_ context.Context, in coreapi.WarmupReceiptInput) (coreapi.WarmupEngagePlan, error) {
	w.receipts = append(w.receipts, in)
	if w.recordErr != nil {
		return coreapi.WarmupEngagePlan{}, w.recordErr
	}
	return w.plan, nil
}

func (w *warmupStubCore) RecordWarmupTokenFailure(_ context.Context, _, _, fingerprint, reasonCode string) error {
	w.tokenFailures = append(w.tokenFailures, reasonCode+":"+fingerprint)
	return nil
}

func (w *warmupStubCore) RecordWarmupHardBounce(_ context.Context, _, messageID, observerMailbox string) (bool, error) {
	w.hardBounceIDs = append(w.hardBounceIDs, messageID)
	w.hardBounceObservers = append(w.hardBounceObservers, observerMailbox)
	return w.hardBounceMatched, nil
}

// Compile-time proof that the stub still satisfies the optional capability. A
// signature change previously made this assertion fail at RUNTIME instead, and the
// poller silently fell through to campaign classification — which is precisely the
// harm the capability exists to prevent.
var _ coreapi.WarmupEvidenceClient = (*warmupStubCore)(nil)

// spyEngageEnqueuer records warmup:engage enqueues instead of touching Redis.
type spyEngageEnqueuer struct {
	calls []engageCall
	err   error
}

type engageCall struct {
	receiptID   string
	workspaceID string
	delay       time.Duration
}

func (s *spyEngageEnqueuer) EnqueueWarmupEngageIn(receiptID, workspaceID string, d time.Duration) error {
	s.calls = append(s.calls, engageCall{receiptID, workspaceID, d})
	return s.err
}

// warmupToken signs a receipt token for the given workspace/send, mirroring what
// a warmup send stamps on X-Inroad-Warmup.
func warmupToken(t *testing.T, secret []byte, workspaceID, sendID string) string {
	t.Helper()
	return warmup.Sign(warmup.Payload{WorkspaceID: workspaceID, WarmupSendID: sendID, FromMailbox: "partner-mb"}, secret)
}

// warmupMsg builds an inbound message carrying an X-Inroad-Warmup header, a
// Message-ID, and an In-Reply-To that (deliberately) matches a seeded send — so a
// test proving isolation can assert the message is NOT classified even though it
// WOULD match the reply path.
func warmupMsg(t *testing.T, uid uint32, token, messageID, inReplyTo string) mail.InboundMessage {
	t.Helper()
	raw := "From: partner@warm.test\nTo: me@mb.test\nSubject: Re: catching up\n" +
		"Message-ID: " + messageID + "\nIn-Reply-To: " + inReplyTo +
		"\nX-Inroad-Warmup: " + token + "\n\nGreat, talk soon.\n"
	return inboundMsg(t, uid, raw)
}

func newWarmupCore(t *testing.T) *warmupStubCore {
	t.Helper()
	return &warmupStubCore{
		stubCore: &stubCore{
			job: coreapi.InboxPollJob{UIDValidity: 5, LastSeenUID: 10},
			// A send the warmup message's In-Reply-To matches — present so the
			// isolation tests prove warmup mail is NOT classified despite matching.
			sendRefs: map[string]coreapi.SendRef{"<root@x>": {SendID: "s1", EnrollmentID: "e1", ContactEmail: "a@b.io"}},
		},
		plan: coreapi.WarmupEngagePlan{ReceiptID: "rcpt-1", DoMarkRead: true, EngageAfter: 90 * time.Second},
	}
}

func runWarmupPoll(t *testing.T, core coreapi.Client, reader mail.InboxReader, enq WarmupEngageEnqueuer) error {
	t.Helper()
	return PollHandler(core, reader, nil, nil, replyclassify.New(nil), warmupSecret, enq)(context.Background(), pollTask(t))
}

// TestPollInboxWarmupRecordedEngagedAndIsolated is the headline: a verified
// warmup message in the INBOX is recorded (placement "inbox"), enqueues
// warmup:engage after the plan's dwell, and is NEVER classified — no MarkReplied
// even though its In-Reply-To matches a seeded send (spec §9.4 isolation).
func TestPollInboxWarmupRecordedEngagedAndIsolated(t *testing.T) {
	core := newWarmupCore(t)
	enq := &spyEngageEnqueuer{}
	token := warmupToken(t, warmupSecret, pollWS, "send-1")
	reader := &fakeReader{
		uidValidity: 5, uidNext: 12,
		msgs: []mail.InboundMessage{warmupMsg(t, 11, token, "<wm-1@warm>", "<root@x>")},
	}

	if err := runWarmupPoll(t, core, reader, enq); err != nil {
		t.Fatal(err)
	}

	if len(core.receipts) != 1 {
		t.Fatalf("expected 1 warmup receipt recorded, got %d", len(core.receipts))
	}
	got := core.receipts[0]
	if got.WorkspaceID != pollWS || got.RecipientMailbox != pollMailbox {
		t.Errorf("receipt ws/recipient = %q/%q, want %q/%q", got.WorkspaceID, got.RecipientMailbox, pollWS, pollMailbox)
	}
	if got.WarmupSendID != "send-1" {
		t.Errorf("receipt WarmupSendID = %q, want send-1 (from verified token)", got.WarmupSendID)
	}
	if got.Placement != placementInbox || got.SourceFolder != sourceFolderInbox {
		t.Errorf("receipt placement/folder = %q/%q, want %q/%q", got.Placement, got.SourceFolder, placementInbox, sourceFolderInbox)
	}
	if got.MessageID != "<wm-1@warm>" {
		t.Errorf("receipt MessageID = %q, want <wm-1@warm>", got.MessageID)
	}
	if len(enq.calls) != 1 || enq.calls[0].receiptID != "rcpt-1" || enq.calls[0].delay != 90*time.Second {
		t.Errorf("expected engage enqueue (rcpt-1, 90s), got %+v", enq.calls)
	}
	if enq.calls[0].workspaceID != pollWS {
		t.Errorf("engage workspace = %q, want %q", enq.calls[0].workspaceID, pollWS)
	}
	// ISOLATION: warmup mail must never touch campaign classification.
	if len(core.replied) != 0 || len(core.bounced) != 0 || len(core.recorded) != 0 || len(core.unsubscribed) != 0 {
		t.Fatalf("warmup mail must NOT be classified; got replied=%v bounced=%v recorded=%v unsub=%v",
			core.replied, core.bounced, core.recorded, core.unsubscribed)
	}
	// The INBOX cursor still advances past the scanned window.
	if !core.cursorSet {
		t.Fatal("cursor must advance after a warmup-only pass")
	}
}

// TestPollJunkWarmupRecordedAsSpam proves the spam-placement scan: a verified
// warmup message found in the junk folder is recorded with placement "spam" and
// the resolved source folder, and enqueues engagement.
func TestPollJunkWarmupRecordedAsSpam(t *testing.T) {
	core := newWarmupCore(t)
	enq := &spyEngageEnqueuer{}
	token := warmupToken(t, warmupSecret, pollWS, "send-2")
	reader := &fakeReader{
		uidValidity: 5, uidNext: 12, // empty INBOX batch (uidNext just above LastSeenUID)
		junkFolder: "Junk",
		junkMsgs:   []mail.InboundMessage{warmupMsg(t, 0, token, "<wm-2@warm>", "<none@x>")},
	}

	if err := runWarmupPoll(t, core, reader, enq); err != nil {
		t.Fatal(err)
	}
	if !reader.junkFetchCalled {
		t.Fatal("expected the junk folder to be scanned")
	}
	if len(core.receipts) != 1 {
		t.Fatalf("expected 1 spam receipt, got %d", len(core.receipts))
	}
	got := core.receipts[0]
	if got.Placement != placementSpam || got.SourceFolder != "Junk" {
		t.Errorf("receipt placement/folder = %q/%q, want spam/Junk", got.Placement, got.SourceFolder)
	}
	if got.WarmupSendID != "send-2" {
		t.Errorf("receipt WarmupSendID = %q, want send-2", got.WarmupSendID)
	}
	if len(enq.calls) != 1 || enq.calls[0].receiptID != "rcpt-1" {
		t.Errorf("expected engage enqueue for the spam receipt, got %+v", enq.calls)
	}
}

// TestPollForgedWarmupTokenFallsThroughToClassification proves detection is
// verified BEFORE trust: a message with a WELL-FORMED but WRONG-SIGNATURE
// X-Inroad-Warmup header is NOT treated as warmup — it falls through to normal
// classification (its In-Reply-To matches a send → MarkReplied) and records no
// receipt.
func TestPollForgedWarmupTokenFallsThroughToClassification(t *testing.T) {
	core := newWarmupCore(t)
	enq := &spyEngageEnqueuer{}
	forged := warmupToken(t, []byte("attacker-secret"), pollWS, "send-x") // signed with the wrong key
	reader := &fakeReader{
		uidValidity: 5, uidNext: 12,
		msgs: []mail.InboundMessage{warmupMsg(t, 11, forged, "<forged@warm>", "<root@x>")},
	}

	if err := runWarmupPoll(t, core, reader, enq); err != nil {
		t.Fatal(err)
	}
	if len(core.receipts) != 0 {
		t.Fatalf("a forged token must NOT record a warmup receipt, got %d", len(core.receipts))
	}
	if len(enq.calls) != 0 {
		t.Fatalf("a forged token must NOT enqueue engagement, got %+v", enq.calls)
	}
	if len(core.replied) != 1 || core.replied[0] != "e1" {
		t.Fatalf("a forged-token message must be classified normally (MarkReplied e1), got %v", core.replied)
	}
	if len(core.tokenFailures) != 1 || !strings.HasPrefix(core.tokenFailures[0], "invalid_signature:") {
		t.Fatalf("forged token evidence = %v", core.tokenFailures)
	}
}

func TestPollWarmupHardBounceIsAttributedBeforeCampaignBounce(t *testing.T) {
	core := newWarmupCore(t)
	core.hardBounceMatched = true
	reader := &fakeReader{
		uidValidity: 5, uidNext: 12,
		msgs: []mail.InboundMessage{inboundMsg(t, 11, hardBounceDSN)},
	}

	if err := runWarmupPoll(t, core, reader, &spyEngageEnqueuer{}); err != nil {
		t.Fatal(err)
	}
	if len(core.hardBounceIDs) != 1 || core.hardBounceIDs[0] != "<orig@x>" {
		t.Fatalf("warmup DSN ids = %v", core.hardBounceIDs)
	}
	if len(core.bounced) != 0 {
		t.Fatalf("warmup DSN reached campaign bounce handling: %v", core.bounced)
	}
}

// TestPollWrongWorkspaceWarmupTokenFallsThroughToClassification proves the
// workspace pin: a token whose HMAC is VALID but whose signed workspace does not
// match the polled mailbox's workspace is NOT warmup — it falls through to normal
// classification and records no receipt.
func TestPollWrongWorkspaceWarmupTokenFallsThroughToClassification(t *testing.T) {
	core := newWarmupCore(t)
	enq := &spyEngageEnqueuer{}
	crossWS := warmupToken(t, warmupSecret, "another-workspace", "send-y") // correctly signed, wrong workspace
	reader := &fakeReader{
		uidValidity: 5, uidNext: 12,
		msgs: []mail.InboundMessage{warmupMsg(t, 11, crossWS, "<xws@warm>", "<root@x>")},
	}

	if err := runWarmupPoll(t, core, reader, enq); err != nil {
		t.Fatal(err)
	}
	if len(core.receipts) != 0 || len(enq.calls) != 0 {
		t.Fatalf("a wrong-workspace token must be ignored; got receipts=%d enqueues=%d", len(core.receipts), len(enq.calls))
	}
	if len(core.replied) != 1 || core.replied[0] != "e1" {
		t.Fatalf("a wrong-workspace message must be classified normally, got %v", core.replied)
	}
	if len(core.tokenFailures) != 1 || !strings.HasPrefix(core.tokenFailures[0], "workspace_mismatch:") {
		t.Fatalf("wrong-workspace token evidence = %v", core.tokenFailures)
	}
}

// TestPollDuplicateWarmupReceiptDoesNotReEngage proves the re-poll guard: when
// RecordWarmupReceipt returns an empty plan (a duplicate receipt), no engage is
// enqueued (the first receipt already scheduled it).
func TestPollDuplicateWarmupReceiptDoesNotReEngage(t *testing.T) {
	core := newWarmupCore(t)
	core.plan = coreapi.WarmupEngagePlan{} // duplicate → empty plan
	enq := &spyEngageEnqueuer{}
	token := warmupToken(t, warmupSecret, pollWS, "send-3")
	reader := &fakeReader{
		uidValidity: 5, uidNext: 12,
		msgs: []mail.InboundMessage{warmupMsg(t, 11, token, "<wm-3@warm>", "<none@x>")},
	}

	if err := runWarmupPoll(t, core, reader, enq); err != nil {
		t.Fatal(err)
	}
	if len(core.receipts) != 1 {
		t.Fatalf("the receipt upsert still runs (idempotent), got %d", len(core.receipts))
	}
	if len(enq.calls) != 0 {
		t.Fatalf("a duplicate receipt must NOT re-enqueue engagement, got %+v", enq.calls)
	}
}

// TestPollWarmupRecordErrorPropagatesOnInboxPath proves an INBOX-path warmup
// record failure surfaces to asynq (so the task retries from the same cursor)
// and the cursor is NOT advanced — the receipt must not be silently lost.
func TestPollWarmupRecordErrorPropagatesOnInboxPath(t *testing.T) {
	core := newWarmupCore(t)
	core.recordErr = errors.New("db down")
	enq := &spyEngageEnqueuer{}
	token := warmupToken(t, warmupSecret, pollWS, "send-4")
	reader := &fakeReader{
		uidValidity: 5, uidNext: 12,
		msgs: []mail.InboundMessage{warmupMsg(t, 11, token, "<wm-4@warm>", "<none@x>")},
	}

	if err := runWarmupPoll(t, core, reader, enq); !errors.Is(err, core.recordErr) {
		t.Fatalf("expected the record error to propagate, got %v", err)
	}
	if core.cursorSet {
		t.Fatal("a failed INBOX warmup record must not advance the cursor")
	}
}

// TestPollJunkNoFolderDoesNotFailPoll proves a missing junk folder is benign: the
// scan is skipped, the INBOX pass still completes, and the cursor advances.
func TestPollJunkNoFolderDoesNotFailPoll(t *testing.T) {
	core := newWarmupCore(t)
	enq := &spyEngageEnqueuer{}
	reader := &fakeReader{
		uidValidity: 5, uidNext: 12,
		junkErr: mail.ErrNoJunkFolder,
	}

	if err := runWarmupPoll(t, core, reader, enq); err != nil {
		t.Fatalf("a missing junk folder must not fail the poll, got %v", err)
	}
	if !core.cursorSet {
		t.Fatal("the INBOX cursor must still advance when the junk folder is absent")
	}
	if len(core.receipts) != 0 {
		t.Fatalf("no warmup mail present, got %d receipts", len(core.receipts))
	}
}

// fakeSpamGmail is a GmailFetcher that also implements gmailSpamScanner, so the
// gmail branch exercises the SPAM-label placement scan.
type fakeSpamGmail struct {
	inboxMsgs []mail.InboundMessage
	spamMsgs  []mail.InboundMessage
	spamErr   error
}

func (f *fakeSpamGmail) Fetch(_ context.Context, _, _ string, _ int) ([]mail.InboundMessage, string, error) {
	return f.inboxMsgs, "cursor-next", nil
}

func (f *fakeSpamGmail) FetchSpam(_ context.Context, _ string, _ int) ([]mail.InboundMessage, error) {
	return f.spamMsgs, f.spamErr
}

// TestPollGmailSpamWarmupRecordedAsSpam proves the Gmail branch also detects
// spam-placed warmup mail: a warmup message in SPAM is recorded placement "spam"
// with source folder "SPAM".
func TestPollGmailSpamWarmupRecordedAsSpam(t *testing.T) {
	core := &warmupStubCore{
		stubCore: &stubCore{job: coreapi.InboxPollJob{Provider: "gmail", AccessToken: []byte("tok"), Cursor: "1000"}},
		plan:     coreapi.WarmupEngagePlan{ReceiptID: "rcpt-g", EngageAfter: time.Minute},
	}
	enq := &spyEngageEnqueuer{}
	token := warmupToken(t, warmupSecret, pollWS, "send-g")
	gmail := &fakeSpamGmail{spamMsgs: []mail.InboundMessage{warmupMsg(t, 0, token, "<wm-g@warm>", "<none@x>")}}

	if err := PollHandler(core, nil, gmail, nil, replyclassify.New(nil), warmupSecret, enq)(context.Background(), pollTask(t)); err != nil {
		t.Fatal(err)
	}
	if len(core.receipts) != 1 {
		t.Fatalf("expected 1 gmail spam receipt, got %d", len(core.receipts))
	}
	got := core.receipts[0]
	if got.Placement != placementSpam || got.SourceFolder != "SPAM" {
		t.Errorf("gmail spam receipt placement/folder = %q/%q, want spam/SPAM", got.Placement, got.SourceFolder)
	}
	if len(enq.calls) != 1 || enq.calls[0].receiptID != "rcpt-g" {
		t.Errorf("expected engage enqueue for the gmail spam receipt, got %+v", enq.calls)
	}
}

// ---------------------------------------------------------------------------
// Provider-native tabbed placement (Phase 2 slice A). Gmail's Promotions tab IS
// the inbox as far as this poller used to count, so a mailbox whose warmup mail
// reliably landed there reported a 100% inbox rate. Two facts are recorded now:
// WHICH tab (when a provider positively identifies one) and whether the reading
// path could have identified one AT ALL.
// ---------------------------------------------------------------------------

// withCategory is the provider tab mail.GmailReader resolved from labelIds. IMAP
// and Graph always leave it empty, which is why the capability is not inferable
// from it.
func withCategory(msg mail.InboundMessage, category string) mail.InboundMessage {
	msg.PlacementCategory = category
	return msg
}

// The headline: a Gmail message Gmail itself filed under Promotions is recorded
// `tabbed`, not `inbox`. The observation also records that the reader COULD see the
// tab, which is what keeps the tabbed rate's denominator honest.
func TestPollGmailPromotionsIsRecordedAsTabbedAndTabCapable(t *testing.T) {
	core := &warmupStubCore{
		stubCore: &stubCore{job: coreapi.InboxPollJob{Provider: "gmail", AccessToken: []byte("tok"), Cursor: "1000"}},
		plan:     coreapi.WarmupEngagePlan{ReceiptID: "rcpt-tab", EngageAfter: time.Minute},
	}
	token := warmupToken(t, warmupSecret, pollWS, "send-tab")
	gmail := &fakeSpamGmail{inboxMsgs: []mail.InboundMessage{
		withCategory(warmupMsg(t, 0, token, "<wm-tab@warm>", "<none@x>"), "promotions"),
	}}

	if err := PollHandler(core, nil, gmail, nil, replyclassify.New(nil), warmupSecret, &spyEngageEnqueuer{})(context.Background(), pollTask(t)); err != nil {
		t.Fatal(err)
	}
	if len(core.receipts) != 1 {
		t.Fatalf("expected 1 receipt, got %d", len(core.receipts))
	}
	got := core.receipts[0]
	if got.Placement != placementTabbed {
		t.Errorf("placement = %q, want %q: a Promotions landing is not a primary-inbox landing", got.Placement, placementTabbed)
	}
	if !got.TabCapable {
		t.Error("TabCapable = false for a Gmail reader that just read a category label")
	}
}

// The other half of the same reader, and the arm that makes a Gmail mailbox's
// tabbed rate mean anything: a message Gmail filed in the PRIMARY inbox is `inbox`
// with the capability still TRUE. Gmail positively told us there was no tab, so the
// observation belongs in the denominator.
func TestPollGmailPrimaryInboxStaysInboxButRemainsTabCapable(t *testing.T) {
	core := &warmupStubCore{
		stubCore: &stubCore{job: coreapi.InboxPollJob{Provider: "gmail", AccessToken: []byte("tok"), Cursor: "1000"}},
		plan:     coreapi.WarmupEngagePlan{ReceiptID: "rcpt-primary", EngageAfter: time.Minute},
	}
	token := warmupToken(t, warmupSecret, pollWS, "send-primary")
	gmail := &fakeSpamGmail{inboxMsgs: []mail.InboundMessage{
		warmupMsg(t, 0, token, "<wm-primary@warm>", "<none@x>"), // no category: the primary tab
	}}

	if err := PollHandler(core, nil, gmail, nil, replyclassify.New(nil), warmupSecret, &spyEngageEnqueuer{})(context.Background(), pollTask(t)); err != nil {
		t.Fatal(err)
	}
	if len(core.receipts) != 1 {
		t.Fatalf("expected 1 receipt, got %d", len(core.receipts))
	}
	got := core.receipts[0]
	if got.Placement != placementInbox {
		t.Errorf("placement = %q, want inbox", got.Placement)
	}
	if !got.TabCapable {
		t.Error("TabCapable = false: Gmail reported no category, which is evidence of the primary inbox, not an absence of evidence")
	}
}

// IMAP has no concept of a tab, so an inbox landing there is `inbox` with the
// capability FALSE. Recording true would pool observations that structurally cannot
// report a tab into the rate's denominator and dilute it toward zero — making an
// untested pool read clean, which is the defect the bounce denominators had.
func TestPollIMAPInboxIsNotTabCapable(t *testing.T) {
	core := newWarmupCore(t)
	token := warmupToken(t, warmupSecret, pollWS, "send-imap")
	reader := &fakeReader{
		uidValidity: 5, uidNext: 12,
		msgs: []mail.InboundMessage{warmupMsg(t, 11, token, "<wm-imap@warm>", "<none@x>")},
	}

	if err := runWarmupPoll(t, core, reader, &spyEngageEnqueuer{}); err != nil {
		t.Fatal(err)
	}
	if len(core.receipts) != 1 {
		t.Fatalf("expected 1 receipt, got %d", len(core.receipts))
	}
	got := core.receipts[0]
	if got.Placement != placementInbox {
		t.Errorf("placement = %q, want inbox", got.Placement)
	}
	if got.TabCapable {
		t.Error("TabCapable = true over IMAP, where a tab is not observable at all")
	}
}

// m365 is not tab-capable either. Graph's nearest concept,
// inferenceClassification (focused|other), is a per-user RELEVANCE guess rather
// than a delivery category; reporting it as a tab would put two meanings in one
// column.
func TestPollM365InboxIsNotTabCapable(t *testing.T) {
	core := &warmupStubCore{
		stubCore: &stubCore{job: coreapi.InboxPollJob{Provider: "m365", AccessToken: []byte("tok"), Cursor: "delta-old"}},
		plan:     coreapi.WarmupEngagePlan{ReceiptID: "rcpt-m365", EngageAfter: time.Minute},
	}
	token := warmupToken(t, warmupSecret, pollWS, "send-m365")
	graph := &fakeJunkGraph{inboxMsgs: []mail.InboundMessage{warmupMsg(t, 0, token, "<wm-m365@warm>", "<none@x>")}}

	if err := PollHandler(core, nil, nil, graph, replyclassify.New(nil), warmupSecret, &spyEngageEnqueuer{})(context.Background(), pollTask(t)); err != nil {
		t.Fatal(err)
	}
	if len(core.receipts) != 1 {
		t.Fatalf("expected 1 receipt, got %d", len(core.receipts))
	}
	if core.receipts[0].TabCapable {
		t.Error("TabCapable = true for a Graph reader, which reports relevance rather than tabs")
	}
}

// Spam wins over any tab, and the poller must not rely on the transport having
// cleared the category for it: the folder it was SCANNING is what decides the
// placement. A spam-foldered message is not in the inbox at all, so no tab applies.
func TestPollGmailSpamWithACategoryIsStillSpam(t *testing.T) {
	core := &warmupStubCore{
		stubCore: &stubCore{job: coreapi.InboxPollJob{Provider: "gmail", AccessToken: []byte("tok"), Cursor: "1000"}},
		plan:     coreapi.WarmupEngagePlan{ReceiptID: "rcpt-spam-tab", EngageAfter: time.Minute},
	}
	token := warmupToken(t, warmupSecret, pollWS, "send-spam-tab")
	gmail := &fakeSpamGmail{spamMsgs: []mail.InboundMessage{
		withCategory(warmupMsg(t, 0, token, "<wm-spam-tab@warm>", "<none@x>"), "promotions"),
	}}

	if err := PollHandler(core, nil, gmail, nil, replyclassify.New(nil), warmupSecret, &spyEngageEnqueuer{})(context.Background(), pollTask(t)); err != nil {
		t.Fatal(err)
	}
	if len(core.receipts) != 1 {
		t.Fatalf("expected 1 receipt, got %d", len(core.receipts))
	}
	if got := core.receipts[0].Placement; got != placementSpam {
		t.Errorf("placement = %q, want spam: a tab cannot apply to a message that is not in the inbox", got)
	}
}

// The whole placement decision, pure: which folder the path was scanning, and what
// the provider said about a tab.
func TestWarmupPlacementResolvesTheFolderAndTheTab(t *testing.T) {
	cases := []struct {
		name     string
		path     readingPath
		category string
		want     string
	}{
		{"inbox with no tab", inboxPath(true), "", placementInbox},
		{"inbox with a tab", inboxPath(true), "promotions", placementTabbed},
		{"a tab reported by a path that cannot see tabs is still honoured", inboxPath(false), "promotions", placementTabbed},
		{"junk with no tab", junkPath("Junk", false), "", placementSpam},
		{"junk with a tab is spam", junkPath("SPAM", true), "promotions", placementSpam},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := warmupPlacement(tc.path, tc.category); got != tc.want {
				t.Errorf("warmupPlacement(%+v, %q) = %q, want %q", tc.path, tc.category, got, tc.want)
			}
		})
	}
}

// fakeJunkGraph is a GraphFetcher that also implements graphJunkScanner, so the
// m365 branch exercises the JunkEmail-folder placement scan (the Graph parallel of
// fakeSpamGmail).
type fakeJunkGraph struct {
	inboxMsgs []mail.InboundMessage
	junkMsgs  []mail.InboundMessage
	junkErr   error
}

func (f *fakeJunkGraph) Fetch(_ context.Context, _, _ string, _ int) ([]mail.InboundMessage, string, error) {
	return f.inboxMsgs, "delta-next", nil
}

func (f *fakeJunkGraph) FetchJunk(_ context.Context, _ string, _ int) ([]mail.InboundMessage, error) {
	return f.junkMsgs, f.junkErr
}

// TestPollM365JunkWarmupRecordedAsSpam proves the m365 branch also detects
// spam-placed warmup mail: a warmup message in the JunkEmail folder is recorded
// placement "spam" with source folder "JunkEmail" (the Graph parallel of the Gmail
// SPAM-label test).
func TestPollM365JunkWarmupRecordedAsSpam(t *testing.T) {
	core := &warmupStubCore{
		stubCore: &stubCore{job: coreapi.InboxPollJob{Provider: "m365", AccessToken: []byte("tok"), Cursor: "delta-old"}},
		plan:     coreapi.WarmupEngagePlan{ReceiptID: "rcpt-m", EngageAfter: time.Minute},
	}
	enq := &spyEngageEnqueuer{}
	token := warmupToken(t, warmupSecret, pollWS, "send-m")
	graph := &fakeJunkGraph{junkMsgs: []mail.InboundMessage{warmupMsg(t, 0, token, "<wm-m@warm>", "<none@x>")}}

	if err := PollHandler(core, nil, nil, graph, replyclassify.New(nil), warmupSecret, enq)(context.Background(), pollTask(t)); err != nil {
		t.Fatal(err)
	}
	if len(core.receipts) != 1 {
		t.Fatalf("expected 1 m365 junk receipt, got %d", len(core.receipts))
	}
	got := core.receipts[0]
	if got.Placement != placementSpam || got.SourceFolder != "JunkEmail" {
		t.Errorf("m365 junk receipt placement/folder = %q/%q, want spam/JunkEmail", got.Placement, got.SourceFolder)
	}
	if got.WarmupSendID != "send-m" {
		t.Errorf("receipt WarmupSendID = %q, want send-m", got.WarmupSendID)
	}
	if len(enq.calls) != 1 || enq.calls[0].receiptID != "rcpt-m" {
		t.Errorf("expected engage enqueue for the m365 junk receipt, got %+v", enq.calls)
	}
}

// TestPollInboxWarmupEnqueueFailureThenRetryReEnqueues exercises the self-heal at the
// poll seam: an INBOX warmup enqueue that FAILS (spyEngageEnqueuer.err) fails the poll
// and does NOT advance the cursor, so asynq retries. On the retry, RecordWarmupReceipt
// re-returns the SAME plan for the still-unengaged receipt (the coreapi self-heal,
// modeled here by the stub returning the plan again) and, with the enqueue now
// succeeding, engagement is re-enqueued and the poll completes. Without the self-heal,
// the retry's duplicate receipt would yield an empty plan and the engage would be lost.
func TestPollInboxWarmupEnqueueFailureThenRetryReEnqueues(t *testing.T) {
	core := newWarmupCore(t) // plan.ReceiptID == "rcpt-1"
	enq := &spyEngageEnqueuer{err: errors.New("redis down")}
	token := warmupToken(t, warmupSecret, pollWS, "send-5")
	msg := warmupMsg(t, 11, token, "<wm-5@warm>", "<none@x>")

	// Poll 1: the enqueue fails, so the whole poll fails and the cursor is untouched.
	reader := &fakeReader{uidValidity: 5, uidNext: 12, msgs: []mail.InboundMessage{msg}}
	if err := runWarmupPoll(t, core, reader, enq); !errors.Is(err, enq.err) {
		t.Fatalf("expected the enqueue error to fail the poll, got %v", err)
	}
	if core.cursorSet {
		t.Fatal("a failed engage enqueue must not advance the INBOX cursor")
	}

	// Retry: enqueue now succeeds. The receipt is still unengaged, so the plan is
	// re-returned and re-enqueued; the poll completes and advances the cursor.
	enq.err = nil
	reader2 := &fakeReader{uidValidity: 5, uidNext: 12, msgs: []mail.InboundMessage{msg}}
	if err := runWarmupPoll(t, core, reader2, enq); err != nil {
		t.Fatalf("retry poll: %v", err)
	}
	if len(enq.calls) != 2 || enq.calls[1].receiptID != "rcpt-1" {
		t.Fatalf("expected the engage to be re-enqueued on retry, got %+v", enq.calls)
	}
	if !core.cursorSet {
		t.Fatal("the retry poll must advance the INBOX cursor once engagement is enqueued")
	}
}

// TestPollJunkWarmupEnqueueFailureDoesNotFailPoll exercises spyEngageEnqueuer.err on
// the junk path: unlike the INBOX path, a junk-scan enqueue failure is best-effort —
// it is logged, NOT propagated — so the poll still succeeds and the INBOX cursor
// advances. The stateless rescan re-observes the (still unengaged) receipt next poll
// and the coreapi self-heal re-returns the plan, so the engage is retried, not lost.
func TestPollJunkWarmupEnqueueFailureDoesNotFailPoll(t *testing.T) {
	core := newWarmupCore(t)
	enq := &spyEngageEnqueuer{err: errors.New("redis down")}
	token := warmupToken(t, warmupSecret, pollWS, "send-6")
	reader := &fakeReader{
		uidValidity: 5, uidNext: 12,
		junkFolder: "Junk",
		junkMsgs:   []mail.InboundMessage{warmupMsg(t, 0, token, "<wm-6@warm>", "<none@x>")},
	}

	if err := runWarmupPoll(t, core, reader, enq); err != nil {
		t.Fatalf("a junk-path enqueue failure must not fail the poll, got %v", err)
	}
	if !core.cursorSet {
		t.Fatal("the INBOX cursor must still advance despite a junk enqueue failure")
	}
	if len(enq.calls) != 1 {
		t.Fatalf("expected one (failed) junk enqueue attempt, got %+v", enq.calls)
	}
}

// TestPollJunkWarmupRecordErrorDoesNotFailPoll proves a junk-path RecordWarmupReceipt
// error is swallowed (logged) and the poll still succeeds and advances the cursor —
// the best-effort junk scan never holds back the INBOX cursor. No engage is enqueued
// because the record failed before a plan was produced.
func TestPollJunkWarmupRecordErrorDoesNotFailPoll(t *testing.T) {
	core := newWarmupCore(t)
	core.recordErr = errors.New("db down")
	enq := &spyEngageEnqueuer{}
	token := warmupToken(t, warmupSecret, pollWS, "send-7")
	reader := &fakeReader{
		uidValidity: 5, uidNext: 12,
		junkFolder: "Junk",
		junkMsgs:   []mail.InboundMessage{warmupMsg(t, 0, token, "<wm-7@warm>", "<none@x>")},
	}

	if err := runWarmupPoll(t, core, reader, enq); err != nil {
		t.Fatalf("a junk-path record error must be swallowed, got %v", err)
	}
	if !core.cursorSet {
		t.Fatal("the INBOX cursor must still advance when a junk record errors")
	}
	if len(enq.calls) != 0 {
		t.Fatalf("a failed junk record must not enqueue engagement, got %+v", enq.calls)
	}
}
