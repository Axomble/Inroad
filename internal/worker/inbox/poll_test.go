package inbox

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/queue"
	"github.com/inroad/inroad/internal/platform/replyclassify"
)

// fakeReader is a test double for mail.InboxReader (and, via FetchJunk, the
// optional imapJunkScanner). The junk* fields drive the best-effort spam-folder
// scan; their zero values (no messages, nil error) make FetchJunk a clean no-op,
// so the many non-warmup poll tests are unaffected by the junk-scan step.
type fakeReader struct {
	uidValidity uint32
	uidNext     uint32
	stateErr    error

	msgs     []mail.InboundMessage
	fetchErr error

	fetchCalled bool
	sinceUID    uint32

	junkMsgs        []mail.InboundMessage
	junkFolder      string
	junkErr         error
	junkFetchCalled bool
}

func (f *fakeReader) CurrentState(mail.IMAPConfig) (uint32, uint32, error) {
	return f.uidValidity, f.uidNext, f.stateErr
}

func (f *fakeReader) Fetch(_ mail.IMAPConfig, sinceUID uint32, _ int) ([]mail.InboundMessage, uint32, error) {
	f.fetchCalled = true
	f.sinceUID = sinceUID
	if f.fetchErr != nil {
		return nil, 0, f.fetchErr
	}
	return f.msgs, f.uidValidity, nil
}

func (f *fakeReader) FetchJunk(_ mail.IMAPConfig, _ int) ([]mail.InboundMessage, string, error) {
	f.junkFetchCalled = true
	if f.junkErr != nil {
		return nil, "", f.junkErr
	}
	return f.junkMsgs, f.junkFolder, nil
}

// stubCore embeds coreapi.Client so it satisfies the interface; only the
// methods the poll/sweep handlers call are implemented — mirrors
// sequence.stubCore. Any other call panics, which is what we want if a
// handler unexpectedly reaches for one.
type stubCore struct {
	coreapi.Client
	job    coreapi.InboxPollJob
	jobErr error

	sendRefs map[string]coreapi.SendRef

	cursorSet      bool
	cursorUID      uint32
	cursorValidity uint32

	cursorStringSet bool
	cursorString    string

	replied        []string  // enrollmentIDs passed to MarkReplied
	repliedClass   []string  // class arg parallel to replied
	repliedSource  []string  // source arg parallel to replied
	repliedConf    []float64 // confidence arg parallel to replied
	unsubscribed   []string  // enrollmentIDs passed to MarkUnsubscribed
	unsubEmail     []string  // email arg parallel to unsubscribed
	recorded       []string  // enrollmentIDs passed to RecordReplyClass
	recordedClass  []string  // class arg parallel to recorded
	recordedSource []string  // source arg parallel to recorded
	recordedConf   []float64 // confidence arg parallel to recorded
	bounced        []bouncedCall
	captured       []coreapi.CRMReplyInput

	mailboxes []coreapi.MailboxRef
	listErr   error
}

type bouncedCall struct {
	enrollmentID string
	email        string
	hard         bool
}

func (s *stubCore) GetInboxPollJob(context.Context, string, string) (coreapi.InboxPollJob, error) {
	return s.job, s.jobErr
}

func (s *stubCore) SetInboxCursor(_ context.Context, _, _ string, uid, validity uint32) error {
	s.cursorSet = true
	s.cursorUID, s.cursorValidity = uid, validity
	return nil
}

func (s *stubCore) SetInboxCursorString(_ context.Context, _, _, cursor string) error {
	s.cursorStringSet = true
	s.cursorString = cursor
	return nil
}

// fakeGmailReader is a test double for the GmailFetcher seam. It records the
// cursor it was resumed from and returns canned messages + a new cursor.
type fakeGmailReader struct {
	msgs      []mail.InboundMessage
	newCursor string
	fetchErr  error

	fetchCalled bool
	sinceCursor string
}

func (f *fakeGmailReader) Fetch(_ context.Context, _, sinceHistoryID string, _ int) ([]mail.InboundMessage, string, error) {
	f.fetchCalled = true
	f.sinceCursor = sinceHistoryID
	if f.fetchErr != nil {
		return nil, "", f.fetchErr
	}
	return f.msgs, f.newCursor, nil
}

func (s *stubCore) FindSendByMessageID(_ context.Context, _, messageID string) (coreapi.SendRef, error) {
	if ref, ok := s.sendRefs[messageID]; ok {
		return ref, nil
	}
	return coreapi.SendRef{}, coreapi.ErrNoMatch
}

func (s *stubCore) MarkReplied(_ context.Context, enrollmentID, _, class, source string, confidence float64) error {
	s.replied = append(s.replied, enrollmentID)
	s.repliedClass = append(s.repliedClass, class)
	s.repliedSource = append(s.repliedSource, source)
	s.repliedConf = append(s.repliedConf, confidence)
	return nil
}

func (s *stubCore) CaptureCRMReply(_ context.Context, in coreapi.CRMReplyInput) error {
	s.captured = append(s.captured, in)
	return nil
}

func (s *stubCore) MarkUnsubscribed(_ context.Context, enrollmentID, _, email string) error {
	s.unsubscribed = append(s.unsubscribed, enrollmentID)
	s.unsubEmail = append(s.unsubEmail, email)
	return nil
}

func (s *stubCore) RecordReplyClass(_ context.Context, enrollmentID, _, class, source string, confidence float64) error {
	s.recorded = append(s.recorded, enrollmentID)
	s.recordedClass = append(s.recordedClass, class)
	s.recordedSource = append(s.recordedSource, source)
	s.recordedConf = append(s.recordedConf, confidence)
	return nil
}

func (s *stubCore) MarkBounced(_ context.Context, enrollmentID, _, email string, hard bool) error {
	s.bounced = append(s.bounced, bouncedCall{enrollmentID, email, hard})
	return nil
}

func (s *stubCore) ListActiveMailboxes(context.Context) ([]coreapi.MailboxRef, error) {
	return s.mailboxes, s.listErr
}

// fakeInboxCapture is a test double for coreapi.InboxCaptureClient: it records
// every stored message for assertion, mirroring stubCore's recording
// convention. Kept as its OWN type (not a stubCore method) so a test can drive
// a core that does NOT implement InboxCaptureClient (plain *stubCore) to prove
// the feature-detection type assertion in processMessage never panics.
type fakeInboxCapture struct {
	stored []coreapi.InboxMessageInput
}

func (f *fakeInboxCapture) StoreInboundMessage(_ context.Context, in coreapi.InboxMessageInput) error {
	f.stored = append(f.stored, in)
	return nil
}

// coreWithInboxCapture composes a *stubCore with a *fakeInboxCapture so the
// resulting value satisfies BOTH coreapi.Client (promoted from stubCore) and
// coreapi.InboxCaptureClient (promoted from fakeInboxCapture) — mirroring how
// the real inprocess client implements both interfaces on one concrete type.
type coreWithInboxCapture struct {
	*stubCore
	*fakeInboxCapture
}

func pollTask(t *testing.T) *asynq.Task {
	t.Helper()
	b, err := json.Marshal(queue.InboxPollPayload{MailboxID: "mb1", WorkspaceID: "ws1"})
	if err != nil {
		t.Fatal(err)
	}
	return asynq.NewTask(queue.TaskInboxPoll, b)
}

// inboundMsg parses a raw RFC 5322 fixture into the shape Fetch returns.
func inboundMsg(t *testing.T, uid uint32, raw string) mail.InboundMessage {
	t.Helper()
	hdr, ct, body := parseFixture(t, raw)
	return mail.InboundMessage{UID: uid, Header: hdr, ContentType: ct, Body: body}
}

func replyFixture(inReplyTo string) string {
	return "From: alice@example.com\nTo: bob@example.com\nSubject: Re: Hello\nIn-Reply-To: " +
		inReplyTo + "\n\nSounds good.\n"
}

// replyWith builds a human reply fixture with a caller-chosen subject and body,
// so a test can drive a specific classifier verdict (OOO subject, unsubscribe
// body, sentiment body, …).
func replyWith(subject, body, inReplyTo string) string {
	return "From: alice@example.com\nTo: bob@example.com\nSubject: " + subject +
		"\nIn-Reply-To: " + inReplyTo + "\n\n" + body + "\n"
}

func autoReplyFixture(inReplyTo string) string {
	// Auto-Submitted: auto-generated with a non-OOO subject → Layer 1 classifies
	// this auto_reply (an "auto-replied" value or an OOO subject would instead
	// resolve to out_of_office).
	return "From: bot@example.com\nTo: bob@example.com\nSubject: Re: Hello\nIn-Reply-To: " +
		inReplyTo + "\nAuto-Submitted: auto-generated\n\nThis is an automated response.\n"
}

func runPoll(t *testing.T, core coreapi.Client, reader mail.InboxReader) error {
	t.Helper()
	// nil API readers: every runPoll test drives the smtp/IMAP path (job.Provider
	// defaults to "", so neither the gmail nor the m365 branch is taken and the
	// readers are unused). Layer-3 model unwired (New(nil)), like production. A nil
	// warmup secret + no-op enqueuer: these tests carry no X-Inroad-Warmup header,
	// so the warmup hook never fires and the classification path is unchanged.
	return PollHandler(core, reader, nil, nil, replyclassify.New(nil), nil, noopEngageEnqueuer{})(context.Background(), pollTask(t))
}

func runGmailPoll(t *testing.T, core coreapi.Client, gmail GmailFetcher) error {
	t.Helper()
	return PollHandler(core, nil, gmail, nil, replyclassify.New(nil), nil, noopEngageEnqueuer{})(context.Background(), pollTask(t))
}

func runGraphPoll(t *testing.T, core coreapi.Client, graph GraphFetcher) error {
	t.Helper()
	return PollHandler(core, nil, nil, graph, replyclassify.New(nil), nil, noopEngageEnqueuer{})(context.Background(), pollTask(t))
}

// noopEngageEnqueuer satisfies WarmupEngageEnqueuer for tests that drive
// non-warmup mail (the hook never enqueues). A warmup-detection test uses a
// recording spy instead (see poll_warmup_test.go).
type noopEngageEnqueuer struct{}

func (noopEngageEnqueuer) EnqueueWarmupEngageIn(string, string, time.Duration) error { return nil }

func TestPollFirstPollBaselinesWithoutFetching(t *testing.T) {
	// job.UIDValidity == 0 means this mailbox has never been polled: baseline
	// to uidNext-1 and process nothing (don't treat the pre-existing inbox as
	// a flood of replies).
	core := &stubCore{job: coreapi.InboxPollJob{UIDValidity: 0, LastSeenUID: 0}}
	reader := &fakeReader{uidValidity: 100, uidNext: 51}
	if err := runPoll(t, core, reader); err != nil {
		t.Fatal(err)
	}
	if reader.fetchCalled {
		t.Fatal("first poll must not fetch any messages")
	}
	if !core.cursorSet || core.cursorUID != 50 || core.cursorValidity != 100 {
		t.Fatalf("expected cursor baselined to (50, 100), got (%d, %d) set=%v", core.cursorUID, core.cursorValidity, core.cursorSet)
	}
	if len(core.replied) != 0 || len(core.bounced) != 0 {
		t.Fatal("first poll must not process any messages")
	}
}

func TestPollUIDValidityChangeReBaselines(t *testing.T) {
	core := &stubCore{job: coreapi.InboxPollJob{UIDValidity: 5, LastSeenUID: 20}}
	reader := &fakeReader{uidValidity: 6, uidNext: 31} // validity changed underneath us
	if err := runPoll(t, core, reader); err != nil {
		t.Fatal(err)
	}
	if reader.fetchCalled {
		t.Fatal("a UIDVALIDITY change must re-baseline, not fetch/process")
	}
	if !core.cursorSet || core.cursorUID != 30 || core.cursorValidity != 6 {
		t.Fatalf("expected cursor re-baselined to (30, 6), got (%d, %d)", core.cursorUID, core.cursorValidity)
	}
}

func TestPollReplyMatchMarksRepliedAndAdvancesCursor(t *testing.T) {
	core := &stubCore{
		job:      coreapi.InboxPollJob{UIDValidity: 5, LastSeenUID: 10},
		sendRefs: map[string]coreapi.SendRef{"<root@x>": {SendID: "s1", EnrollmentID: "e1", ContactEmail: "a@b.io"}},
	}
	reader := &fakeReader{
		uidValidity: 5, uidNext: 12,
		msgs: []mail.InboundMessage{inboundMsg(t, 11, replyFixture("<root@x>"))},
	}
	if err := runPoll(t, core, reader); err != nil {
		t.Fatal(err)
	}
	if len(core.replied) != 1 || core.replied[0] != "e1" {
		t.Fatalf("expected MarkReplied(e1), got %v", core.replied)
	}
	if !core.cursorSet || core.cursorUID != 11 {
		t.Fatalf("expected cursor advanced to 11, got %d set=%v", core.cursorUID, core.cursorSet)
	}
}

func TestPollAutoReplyDoesNotMarkReplied(t *testing.T) {
	core := &stubCore{
		job:      coreapi.InboxPollJob{UIDValidity: 5, LastSeenUID: 10},
		sendRefs: map[string]coreapi.SendRef{"<root@x>": {SendID: "s1", EnrollmentID: "e1", ContactEmail: "a@b.io"}},
	}
	reader := &fakeReader{
		uidValidity: 5, uidNext: 12,
		msgs: []mail.InboundMessage{inboundMsg(t, 11, autoReplyFixture("<root@x>"))},
	}
	if err := runPoll(t, core, reader); err != nil {
		t.Fatal(err)
	}
	if len(core.replied) != 0 || len(core.unsubscribed) != 0 {
		t.Fatalf("an auto-reply must not be treated as an engaged reply, got replied=%v unsub=%v", core.replied, core.unsubscribed)
	}
	if len(core.recorded) != 1 || core.recorded[0] != "e1" || core.recordedClass[0] != replyclassify.ClassAutoReply {
		t.Fatalf("expected RecordReplyClass(e1, auto_reply), got ids=%v classes=%v", core.recorded, core.recordedClass)
	}
	if !core.cursorSet || core.cursorUID != 11 {
		t.Fatal("cursor must still advance past a skipped message")
	}
}

// TestPollOutOfOfficeRecordsClassWithoutStopping locks in the OOO-trap fix: an
// out-of-office auto-reply (recognized by subject, with NO Auto-Submitted
// header) that matches a send is TAGGED via RecordReplyClass but must NOT stop
// the enrollment (no MarkReplied / MarkUnsubscribed) and is counted as skipped,
// not an engaged reply.
func TestPollOutOfOfficeRecordsClassWithoutStopping(t *testing.T) {
	core := &stubCore{
		job:      coreapi.InboxPollJob{UIDValidity: 5, LastSeenUID: 10},
		sendRefs: map[string]coreapi.SendRef{"<root@x>": {SendID: "s1", EnrollmentID: "e1", ContactEmail: "a@b.io"}},
	}
	reader := &fakeReader{
		uidValidity: 5, uidNext: 12,
		msgs: []mail.InboundMessage{inboundMsg(t, 11, replyWith("Out of Office", "I am away until Monday.", "<root@x>"))},
	}
	if err := runPoll(t, core, reader); err != nil {
		t.Fatal(err)
	}
	if len(core.recorded) != 1 || core.recorded[0] != "e1" || core.recordedClass[0] != replyclassify.ClassOutOfOffice {
		t.Fatalf("expected RecordReplyClass(e1, out_of_office), got ids=%v classes=%v", core.recorded, core.recordedClass)
	}
	// An OOO subject is a Layer-1 verdict: source="header", non-zero confidence.
	if core.recordedSource[0] != replyclassify.SourceHeader || core.recordedConf[0] <= 0 {
		t.Fatalf("expected header source + non-zero confidence, got source=%q conf=%v", core.recordedSource[0], core.recordedConf[0])
	}
	if len(core.replied) != 0 || len(core.unsubscribed) != 0 {
		t.Fatalf("an OOO reply must NOT stop the enrollment, got replied=%v unsub=%v", core.replied, core.unsubscribed)
	}
	if !core.cursorSet || core.cursorUID != 11 {
		t.Fatal("cursor must still advance past an OOO reply")
	}
}

// TestPollUnsubscribeReplySuppresses proves a reply-based opt-out is routed to
// MarkUnsubscribed (address suppressed), not MarkReplied, and does not tag via
// RecordReplyClass.
func TestPollUnsubscribeReplySuppresses(t *testing.T) {
	core := &stubCore{
		job:      coreapi.InboxPollJob{UIDValidity: 5, LastSeenUID: 10},
		sendRefs: map[string]coreapi.SendRef{"<root@x>": {SendID: "s1", EnrollmentID: "e1", ContactEmail: "a@b.io"}},
	}
	reader := &fakeReader{
		uidValidity: 5, uidNext: 12,
		msgs: []mail.InboundMessage{inboundMsg(t, 11, replyWith("Re: Hello", "Please unsubscribe me from this list.", "<root@x>"))},
	}
	if err := runPoll(t, core, reader); err != nil {
		t.Fatal(err)
	}
	if len(core.unsubscribed) != 1 || core.unsubscribed[0] != "e1" || core.unsubEmail[0] != "a@b.io" {
		t.Fatalf("expected MarkUnsubscribed(e1, a@b.io), got ids=%v emails=%v", core.unsubscribed, core.unsubEmail)
	}
	if len(core.replied) != 0 || len(core.recorded) != 0 {
		t.Fatalf("an unsubscribe reply must only suppress, got replied=%v recorded=%v", core.replied, core.recorded)
	}
}

// TestPollNegativeReplyMarksRepliedNegative proves "not interested" (which
// contains the positive token "interested") is tagged negative — the
// negation/order fix — and stops the enrollment.
func TestPollNegativeReplyMarksRepliedNegative(t *testing.T) {
	core := &stubCore{
		job:      coreapi.InboxPollJob{UIDValidity: 5, LastSeenUID: 10},
		sendRefs: map[string]coreapi.SendRef{"<root@x>": {SendID: "s1", EnrollmentID: "e1", ContactEmail: "a@b.io"}},
	}
	reader := &fakeReader{
		uidValidity: 5, uidNext: 12,
		msgs: []mail.InboundMessage{inboundMsg(t, 11, replyWith("Re: Hello", "Not interested, thanks.", "<root@x>"))},
	}
	if err := runPoll(t, core, reader); err != nil {
		t.Fatal(err)
	}
	if len(core.replied) != 1 || core.replied[0] != "e1" || core.repliedClass[0] != replyclassify.ClassNegative {
		t.Fatalf("expected MarkReplied(e1, negative), got ids=%v classes=%v", core.replied, core.repliedClass)
	}
	// "not interested" is a Layer-2 verdict: source="lexicon", non-zero confidence.
	if core.repliedSource[0] != replyclassify.SourceLexicon || core.repliedConf[0] <= 0 {
		t.Fatalf("expected lexicon source + non-zero confidence, got source=%q conf=%v", core.repliedSource[0], core.repliedConf[0])
	}
	if len(core.unsubscribed) != 0 || len(core.recorded) != 0 {
		t.Fatalf("a negative reply must only MarkReplied, got unsub=%v recorded=%v", core.unsubscribed, core.recorded)
	}
}

// TestPollPositiveReplyMarksRepliedPositive proves a clear interest reply is
// tagged positive and stops the enrollment.
func TestPollPositiveReplyMarksRepliedPositive(t *testing.T) {
	core := &stubCore{
		job:      coreapi.InboxPollJob{UIDValidity: 5, LastSeenUID: 10},
		sendRefs: map[string]coreapi.SendRef{"<root@x>": {SendID: "s1", EnrollmentID: "e1", ContactEmail: "a@b.io"}},
	}
	reader := &fakeReader{
		uidValidity: 5, uidNext: 12,
		msgs: []mail.InboundMessage{inboundMsg(t, 11, replyWith("Re: Hello", "Sounds great, let's chat this week.", "<root@x>"))},
	}
	if err := runPoll(t, core, reader); err != nil {
		t.Fatal(err)
	}
	if len(core.replied) != 1 || core.replied[0] != "e1" || core.repliedClass[0] != replyclassify.ClassPositive {
		t.Fatalf("expected MarkReplied(e1, positive), got ids=%v classes=%v", core.replied, core.repliedClass)
	}
	if len(core.unsubscribed) != 0 || len(core.recorded) != 0 {
		t.Fatalf("a positive reply must only MarkReplied, got unsub=%v recorded=%v", core.unsubscribed, core.recorded)
	}
	if len(core.captured) != 1 || core.captured[0].SendID != "s1" ||
		core.captured[0].EnrollmentID != "e1" || core.captured[0].ReplyClass != replyclassify.ClassPositive {
		t.Fatalf("positive reply was not captured for CRM: %+v", core.captured)
	}
}

// TestPollAmbiguousReplyMarksRepliedUnknown proves a plain human reply with no
// keyword signal (Layer 3 unwired) still stops the enrollment, tagged unknown.
func TestPollAmbiguousReplyMarksRepliedUnknown(t *testing.T) {
	core := &stubCore{
		job:      coreapi.InboxPollJob{UIDValidity: 5, LastSeenUID: 10},
		sendRefs: map[string]coreapi.SendRef{"<root@x>": {SendID: "s1", EnrollmentID: "e1", ContactEmail: "a@b.io"}},
	}
	reader := &fakeReader{
		uidValidity: 5, uidNext: 12,
		msgs: []mail.InboundMessage{inboundMsg(t, 11, replyWith("Re: Hello", "Thanks for reaching out.", "<root@x>"))},
	}
	if err := runPoll(t, core, reader); err != nil {
		t.Fatal(err)
	}
	if len(core.replied) != 1 || core.replied[0] != "e1" || core.repliedClass[0] != replyclassify.ClassUnknown {
		t.Fatalf("expected MarkReplied(e1, unknown), got ids=%v classes=%v", core.replied, core.repliedClass)
	}
	if len(core.unsubscribed) != 0 || len(core.recorded) != 0 {
		t.Fatalf("an ambiguous reply must only MarkReplied, got unsub=%v recorded=%v", core.unsubscribed, core.recorded)
	}
}

// TestPollOptOutInsideAutomatedMessageIsSuppressed is the compliance headline:
// a message that Layer 1 would call automated (OOO subject) but whose body
// carries an explicit opt-out MUST be routed to MarkUnsubscribed, not merely
// RecordReplyClass — compliance wins over automation.
func TestPollOptOutInsideAutomatedMessageIsSuppressed(t *testing.T) {
	core := &stubCore{
		job:      coreapi.InboxPollJob{UIDValidity: 5, LastSeenUID: 10},
		sendRefs: map[string]coreapi.SendRef{"<root@x>": {SendID: "s1", EnrollmentID: "e1", ContactEmail: "a@b.io"}},
	}
	reader := &fakeReader{
		uidValidity: 5, uidNext: 12,
		msgs: []mail.InboundMessage{inboundMsg(t, 11, replyWith("Out of Office", "I am away, but please remove me from your list.", "<root@x>"))},
	}
	if err := runPoll(t, core, reader); err != nil {
		t.Fatal(err)
	}
	if len(core.unsubscribed) != 1 || core.unsubscribed[0] != "e1" || core.unsubEmail[0] != "a@b.io" {
		t.Fatalf("an opt-out inside an automated message must suppress, got unsub=%v emails=%v", core.unsubscribed, core.unsubEmail)
	}
	if len(core.recorded) != 0 || len(core.replied) != 0 {
		t.Fatalf("compliance must win over automation (no RecordReplyClass/MarkReplied), got recorded=%v replied=%v", core.recorded, core.replied)
	}
}

// TestPollUnsubscribeLegacyDirectSendStillSuppresses proves reply-based
// suppression works for a legacy direct-send match (EnrollmentID == ""): there
// is no enrollment to stop, but the contact address is still suppressed.
func TestPollUnsubscribeLegacyDirectSendStillSuppresses(t *testing.T) {
	core := &stubCore{
		job:      coreapi.InboxPollJob{UIDValidity: 5, LastSeenUID: 10},
		sendRefs: map[string]coreapi.SendRef{"<root@x>": {SendID: "s1", EnrollmentID: "", ContactEmail: "direct@b.io"}},
	}
	reader := &fakeReader{
		uidValidity: 5, uidNext: 12,
		msgs: []mail.InboundMessage{inboundMsg(t, 11, replyWith("Re: Hello", "Please unsubscribe me.", "<root@x>"))},
	}
	if err := runPoll(t, core, reader); err != nil {
		t.Fatal(err)
	}
	if len(core.unsubscribed) != 1 || core.unsubscribed[0] != "" || core.unsubEmail[0] != "direct@b.io" {
		t.Fatalf("expected MarkUnsubscribed(\"\", direct@b.io), got ids=%v emails=%v", core.unsubscribed, core.unsubEmail)
	}
	if len(core.replied) != 0 || len(core.recorded) != 0 {
		t.Fatalf("a legacy-direct-send opt-out must only suppress, got replied=%v recorded=%v", core.replied, core.recorded)
	}
}

func TestPollHardBounceMarksBounced(t *testing.T) {
	core := &stubCore{
		job:      coreapi.InboxPollJob{UIDValidity: 5, LastSeenUID: 10},
		sendRefs: map[string]coreapi.SendRef{"<orig@x>": {SendID: "s1", EnrollmentID: "e1", ContactEmail: "nobody@recipient.example.com"}},
	}
	reader := &fakeReader{
		uidValidity: 5, uidNext: 12,
		msgs: []mail.InboundMessage{inboundMsg(t, 11, hardBounceDSN)},
	}
	if err := runPoll(t, core, reader); err != nil {
		t.Fatal(err)
	}
	if len(core.bounced) != 1 || !core.bounced[0].hard || core.bounced[0].enrollmentID != "e1" {
		t.Fatalf("expected a hard MarkBounced(e1), got %v", core.bounced)
	}
	if len(core.replied) != 0 {
		t.Fatal("a DSN must never also be treated as a reply")
	}
}

// hardBounceDSNWithOptOutBody is a hard-bounce DSN whose human-readable part
// ALSO contains opt-out / rejection language. The DSN branch must fire on the
// multipart/report structure BEFORE the classifier ever sees the body, so those
// words must not turn a bounce into an unsubscribe or a negative reply. Same
// embedded original Message-ID (<orig@x>) as hardBounceDSN.
const hardBounceDSNWithOptOutBody = `From: Mail Delivery System <MAILER-DAEMON@mail.example.com>
To: sender@example.com
Subject: Undelivered Mail Returned to Sender
Content-Type: multipart/report; report-type=delivery-status;
	boundary="BOUNDARY1"
MIME-Version: 1.0

--BOUNDARY1
Content-Description: Notification
Content-Type: text/plain; charset=us-ascii

Please unsubscribe me. Not interested. (This text is bait for the classifier.)

--BOUNDARY1
Content-Description: Delivery report
Content-Type: message/delivery-status

Reporting-MTA: dns; mail.example.com
Arrival-Date: Mon, 1 Jan 2026 10:00:00 -0500

Final-Recipient: rfc822; nobody@recipient.example.com
Action: failed
Status: 5.1.1
Diagnostic-Code: smtp; 550 5.1.1 <nobody@recipient.example.com>: Recipient address rejected: User unknown

--BOUNDARY1
Content-Description: Undelivered Message Headers
Content-Type: message/rfc822-headers

Message-ID: <orig@x>
To: nobody@recipient.example.com
From: sender@example.com
Subject: Hello there

--BOUNDARY1--
`

// TestPollDSNWinsOverOptOutBody is a routing-order regression: a hard-bounce DSN
// whose body also contains "unsubscribe" / "not interested" must be handled by
// the DSN-first branch (MarkBounced) and never reach the classifier, so no
// reply/unsubscribe/record action fires.
func TestPollDSNWinsOverOptOutBody(t *testing.T) {
	core := &stubCore{
		job:      coreapi.InboxPollJob{UIDValidity: 5, LastSeenUID: 10},
		sendRefs: map[string]coreapi.SendRef{"<orig@x>": {SendID: "s1", EnrollmentID: "e1", ContactEmail: "nobody@recipient.example.com"}},
	}
	reader := &fakeReader{
		uidValidity: 5, uidNext: 12,
		msgs: []mail.InboundMessage{inboundMsg(t, 11, hardBounceDSNWithOptOutBody)},
	}
	if err := runPoll(t, core, reader); err != nil {
		t.Fatal(err)
	}
	if len(core.bounced) != 1 || !core.bounced[0].hard || core.bounced[0].enrollmentID != "e1" {
		t.Fatalf("expected a hard MarkBounced(e1), got %v", core.bounced)
	}
	if len(core.replied) != 0 || len(core.unsubscribed) != 0 || len(core.recorded) != 0 {
		t.Fatalf("the DSN branch must win — classifier never reached; got replied=%v unsub=%v recorded=%v",
			core.replied, core.unsubscribed, core.recorded)
	}
}

func TestPollSoftBounceNeitherMarks(t *testing.T) {
	core := &stubCore{
		job:      coreapi.InboxPollJob{UIDValidity: 5, LastSeenUID: 10},
		sendRefs: map[string]coreapi.SendRef{"<orig2@y>": {SendID: "s1", EnrollmentID: "e1", ContactEmail: "someone@contoso.example"}},
	}
	reader := &fakeReader{
		uidValidity: 5, uidNext: 12,
		msgs: []mail.InboundMessage{inboundMsg(t, 11, softBounceDSN)},
	}
	if err := runPoll(t, core, reader); err != nil {
		t.Fatal(err)
	}
	if len(core.bounced) != 0 || len(core.replied) != 0 {
		t.Fatalf("a soft bounce must not stop/suppress, got bounced=%v replied=%v", core.bounced, core.replied)
	}
	if !core.cursorSet || core.cursorUID != 11 {
		t.Fatal("cursor must still advance past a soft bounce")
	}
}

func TestPollNoMatchIsIgnoredButCursorAdvances(t *testing.T) {
	core := &stubCore{job: coreapi.InboxPollJob{UIDValidity: 5, LastSeenUID: 10}}
	reader := &fakeReader{
		uidValidity: 5, uidNext: 12,
		msgs: []mail.InboundMessage{inboundMsg(t, 11, replyFixture("<unknown@x>"))},
	}
	if err := runPoll(t, core, reader); err != nil {
		t.Fatal(err)
	}
	if len(core.replied) != 0 || len(core.bounced) != 0 {
		t.Fatal("an unmatched message must not mark anything")
	}
	if !core.cursorSet || core.cursorUID != 11 {
		t.Fatal("cursor must still advance past an unmatched message")
	}
}

// TestPollZeroMessagesAdvancesCursorPastStalledWindow guards against a
// permanent stall: a successful bounded Fetch has definitively examined
// every UID up to LastSeenUID+fetchBatchSize, regardless of whether any of
// them actually held mail. If every UID in that window is a gap (expunged
// or never assigned) while newer mail sits above the window, the cursor
// must still advance to the scanned-window top — otherwise the next poll
// re-scans the exact same empty range forever and detection silently dies
// for this mailbox.
func TestPollZeroMessagesAdvancesCursorPastStalledWindow(t *testing.T) {
	core := &stubCore{job: coreapi.InboxPollJob{UIDValidity: 5, LastSeenUID: 10}}
	reader := &fakeReader{uidValidity: 5, uidNext: 510} // far more mail above the fetch window
	if err := runPoll(t, core, reader); err != nil {
		t.Fatal(err)
	}
	if !core.cursorSet || core.cursorUID != 10+fetchBatchSize || core.cursorValidity != 5 {
		t.Fatalf("expected cursor advanced to the scanned window top (%d), got %d set=%v", 10+fetchBatchSize, core.cursorUID, core.cursorSet)
	}
}

// TestPollSparseWindowAdvancesToWindowTopNotMessageUID locks in the same
// invariant for a window that isn't entirely empty: one message near the
// bottom of the fetch window must not leave the cursor stuck near the
// bottom either — the whole window was scanned, so the cursor advances to
// its top, not to the last message's UID.
func TestPollSparseWindowAdvancesToWindowTopNotMessageUID(t *testing.T) {
	core := &stubCore{job: coreapi.InboxPollJob{UIDValidity: 5, LastSeenUID: 10}}
	reader := &fakeReader{
		uidValidity: 5, uidNext: 1000, // plenty more mail above the window
		msgs: []mail.InboundMessage{inboundMsg(t, 15, replyFixture("<unknown@x>"))},
	}
	if err := runPoll(t, core, reader); err != nil {
		t.Fatal(err)
	}
	if !core.cursorSet || core.cursorUID != 10+fetchBatchSize {
		t.Fatalf("expected cursor advanced to the scanned window top (%d), not the message's UID, got %d", 10+fetchBatchSize, core.cursorUID)
	}
}

// TestPollFirstPollZeroUIDNextDoesNotUnderflow guards the re-baseline path
// against a misbehaving server reporting UIDNEXT==0 (RFC 3501 says this
// never happens, but the handler must not trust that): uidNext-1 with no
// guard would wrap a uint32 to math.MaxUint32 and permanently wedge the
// mailbox's cursor.
func TestPollFirstPollZeroUIDNextDoesNotUnderflow(t *testing.T) {
	core := &stubCore{job: coreapi.InboxPollJob{UIDValidity: 0}}
	reader := &fakeReader{uidValidity: 1, uidNext: 0}
	if err := runPoll(t, core, reader); err != nil {
		t.Fatal(err)
	}
	if !core.cursorSet || core.cursorUID != 0 {
		t.Fatalf("expected cursor baselined to 0 (not underflowed), got %d set=%v", core.cursorUID, core.cursorSet)
	}
}

// TestPollPropagatesGetInboxPollJobError proves a core-side lookup failure
// surfaces to asynq (so the task retries) rather than silently no-op'ing,
// and that no cursor is persisted on that path.
func TestPollPropagatesGetInboxPollJobError(t *testing.T) {
	want := errors.New("db down")
	core := &stubCore{jobErr: want}
	reader := &fakeReader{}
	if err := runPoll(t, core, reader); !errors.Is(err, want) {
		t.Fatalf("expected core error to propagate, got %v", err)
	}
	if core.cursorSet {
		t.Fatal("a failed GetInboxPollJob must not persist a cursor")
	}
}

// TestPollPropagatesCurrentStateError proves an IMAP CurrentState failure
// (dial/login/select) surfaces to asynq rather than falling through to
// Fetch or the re-baseline path with zero-value uidValidity/uidNext, and
// that no cursor is persisted.
func TestPollPropagatesCurrentStateError(t *testing.T) {
	want := errors.New("imap dial failed")
	core := &stubCore{job: coreapi.InboxPollJob{UIDValidity: 5, LastSeenUID: 10}}
	reader := &fakeReader{stateErr: want}
	if err := runPoll(t, core, reader); !errors.Is(err, want) {
		t.Fatalf("expected reader error to propagate, got %v", err)
	}
	if core.cursorSet {
		t.Fatal("a failed CurrentState must not persist a cursor")
	}
}

// TestPollPropagatesFetchError proves an IMAP Fetch failure surfaces to
// asynq (so the task retries from the same, unmoved cursor) instead of
// silently persisting a partial/zero-value cursor.
func TestPollPropagatesFetchError(t *testing.T) {
	want := errors.New("imap fetch failed")
	core := &stubCore{job: coreapi.InboxPollJob{UIDValidity: 5, LastSeenUID: 10}}
	reader := &fakeReader{uidValidity: 5, uidNext: 12, fetchErr: want}
	if err := runPoll(t, core, reader); !errors.Is(err, want) {
		t.Fatalf("expected reader error to propagate, got %v", err)
	}
	if core.cursorSet {
		t.Fatal("a failed Fetch must not persist a cursor")
	}
}

// TestPollGmailProviderUsesGmailReaderAndSharedClassification proves the
// provider branch: a gmail job resumes the GmailReader from the opaque cursor,
// runs the SAME reply/bounce classification (a reply here marks the enrollment
// replied), and persists the advanced cursor via SetInboxCursorString — never
// touching the IMAP UID cursor path.
func TestPollGmailProviderUsesGmailReaderAndSharedClassification(t *testing.T) {
	core := &stubCore{
		job:      coreapi.InboxPollJob{Provider: "gmail", AccessToken: []byte("tok"), Cursor: "1000"},
		sendRefs: map[string]coreapi.SendRef{"<root@x>": {SendID: "s1", EnrollmentID: "e1", ContactEmail: "a@b.io"}},
	}
	gmail := &fakeGmailReader{
		newCursor: "2000",
		msgs:      []mail.InboundMessage{inboundMsg(t, 0, replyFixture("<root@x>"))},
	}
	if err := runGmailPoll(t, core, gmail); err != nil {
		t.Fatal(err)
	}
	if !gmail.fetchCalled || gmail.sinceCursor != "1000" {
		t.Fatalf("expected GmailReader resumed from cursor 1000, got called=%v since=%q", gmail.fetchCalled, gmail.sinceCursor)
	}
	if len(core.replied) != 1 || core.replied[0] != "e1" {
		t.Fatalf("expected shared classification to MarkReplied(e1), got %v", core.replied)
	}
	if !core.cursorStringSet || core.cursorString != "2000" {
		t.Fatalf("expected opaque cursor advanced to 2000, got %q set=%v", core.cursorString, core.cursorStringSet)
	}
	if core.cursorSet {
		t.Fatal("gmail path must not touch the IMAP UID cursor")
	}
}

// TestPollGmailBounceMarksBounced proves the gmail path shares the DSN parser:
// a bounce DSN fetched by the GmailReader marks the enrollment bounced.
func TestPollGmailBounceMarksBounced(t *testing.T) {
	core := &stubCore{
		job:      coreapi.InboxPollJob{Provider: "gmail", AccessToken: []byte("tok"), Cursor: "1000"},
		sendRefs: map[string]coreapi.SendRef{"<orig@x>": {SendID: "s1", EnrollmentID: "e1", ContactEmail: "nobody@recipient.example.com"}},
	}
	gmail := &fakeGmailReader{
		newCursor: "2000",
		msgs:      []mail.InboundMessage{inboundMsg(t, 0, hardBounceDSN)},
	}
	if err := runGmailPoll(t, core, gmail); err != nil {
		t.Fatal(err)
	}
	if len(core.bounced) != 1 || !core.bounced[0].hard || core.bounced[0].enrollmentID != "e1" {
		t.Fatalf("expected a hard MarkBounced(e1), got %v", core.bounced)
	}
	if !core.cursorStringSet || core.cursorString != "2000" {
		t.Fatalf("expected opaque cursor advanced to 2000, got %q", core.cursorString)
	}
}

// TestPollGmailPropagatesFetchError proves a Gmail Fetch failure surfaces to
// asynq and no cursor is persisted (the pass retries from the same cursor).
func TestPollGmailPropagatesFetchError(t *testing.T) {
	want := errors.New("gmail api down")
	core := &stubCore{job: coreapi.InboxPollJob{Provider: "gmail", AccessToken: []byte("tok"), Cursor: "1000"}}
	gmail := &fakeGmailReader{fetchErr: want}
	if err := runGmailPoll(t, core, gmail); !errors.Is(err, want) {
		t.Fatalf("expected gmail reader error to propagate, got %v", err)
	}
	if core.cursorStringSet {
		t.Fatal("a failed gmail Fetch must not persist a cursor")
	}
}

// TestPollM365ProviderUsesGraphReaderAndSharedClassification proves the m365
// provider branch: a Graph job resumes the GraphReader from the opaque
// delta-link cursor, runs the SAME reply/bounce classification (a reply here
// marks the enrollment replied), and persists the advanced cursor via
// SetInboxCursorString — never touching the IMAP UID cursor path. fakeGmailReader
// satisfies GraphFetcher (identical shape), so it doubles as the Graph fake.
func TestPollM365ProviderUsesGraphReaderAndSharedClassification(t *testing.T) {
	core := &stubCore{
		job:      coreapi.InboxPollJob{Provider: "m365", AccessToken: []byte("tok"), Cursor: "delta-old"},
		sendRefs: map[string]coreapi.SendRef{"<root@x>": {SendID: "s1", EnrollmentID: "e1", ContactEmail: "a@b.io"}},
	}
	graph := &fakeGmailReader{
		newCursor: "delta-new",
		msgs:      []mail.InboundMessage{inboundMsg(t, 0, replyFixture("<root@x>"))},
	}
	if err := runGraphPoll(t, core, graph); err != nil {
		t.Fatal(err)
	}
	if !graph.fetchCalled || graph.sinceCursor != "delta-old" {
		t.Fatalf("expected GraphReader resumed from cursor delta-old, got called=%v since=%q", graph.fetchCalled, graph.sinceCursor)
	}
	if len(core.replied) != 1 || core.replied[0] != "e1" {
		t.Fatalf("expected shared classification to MarkReplied(e1), got %v", core.replied)
	}
	if !core.cursorStringSet || core.cursorString != "delta-new" {
		t.Fatalf("expected opaque cursor advanced to delta-new, got %q set=%v", core.cursorString, core.cursorStringSet)
	}
	if core.cursorSet {
		t.Fatal("m365 path must not touch the IMAP UID cursor")
	}
}

// TestPollM365BounceMarksBounced proves the m365 path shares the DSN parser:
// a bounce DSN fetched by the GraphReader marks the enrollment bounced.
func TestPollM365BounceMarksBounced(t *testing.T) {
	core := &stubCore{
		job:      coreapi.InboxPollJob{Provider: "m365", AccessToken: []byte("tok"), Cursor: "delta-old"},
		sendRefs: map[string]coreapi.SendRef{"<orig@x>": {SendID: "s1", EnrollmentID: "e1", ContactEmail: "nobody@recipient.example.com"}},
	}
	graph := &fakeGmailReader{
		newCursor: "delta-new",
		msgs:      []mail.InboundMessage{inboundMsg(t, 0, hardBounceDSN)},
	}
	if err := runGraphPoll(t, core, graph); err != nil {
		t.Fatal(err)
	}
	if len(core.bounced) != 1 || !core.bounced[0].hard || core.bounced[0].enrollmentID != "e1" {
		t.Fatalf("expected a hard MarkBounced(e1), got %v", core.bounced)
	}
	if !core.cursorStringSet || core.cursorString != "delta-new" {
		t.Fatalf("expected opaque cursor advanced to delta-new, got %q", core.cursorString)
	}
}

// TestPollM365PropagatesFetchError proves a Graph Fetch failure surfaces to
// asynq and no cursor is persisted (the pass retries from the same cursor).
func TestPollM365PropagatesFetchError(t *testing.T) {
	want := errors.New("graph api down")
	core := &stubCore{job: coreapi.InboxPollJob{Provider: "m365", AccessToken: []byte("tok"), Cursor: "delta-old"}}
	graph := &fakeGmailReader{fetchErr: want}
	if err := runGraphPoll(t, core, graph); !errors.Is(err, want) {
		t.Fatalf("expected graph reader error to propagate, got %v", err)
	}
	if core.cursorStringSet {
		t.Fatal("a failed graph Fetch must not persist a cursor")
	}
}

// TestPollPositiveReplyIsAlsoStoredInTheInbox proves a matched reply is stored
// via coreapi.InboxCaptureClient (not just tagged/CRM-captured) — same fixture
// as TestPollPositiveReplyMarksRepliedPositive, wrapped with a
// coreWithInboxCapture so the core also satisfies InboxCaptureClient.
func TestPollPositiveReplyIsAlsoStoredInTheInbox(t *testing.T) {
	core := &stubCore{
		job: coreapi.InboxPollJob{UIDValidity: 5, LastSeenUID: 10},
		sendRefs: map[string]coreapi.SendRef{"<root@x>": {
			SendID: "s1", EnrollmentID: "e1", ContactEmail: "a@b.io",
			MailboxID: "mb1", CampaignID: "camp1", ContactID: "contact1", MessageID: "<root@x>",
		}},
	}
	capture := &fakeInboxCapture{}
	reader := &fakeReader{
		uidValidity: 5, uidNext: 12,
		msgs: []mail.InboundMessage{inboundMsg(t, 11, replyWith("Re: Hello", "Sounds great, let's chat this week.", "<root@x>"))},
	}
	if err := runPoll(t, coreWithInboxCapture{stubCore: core, fakeInboxCapture: capture}, reader); err != nil {
		t.Fatal(err)
	}
	if len(capture.stored) != 1 {
		t.Fatalf("expected 1 stored message, got %d", len(capture.stored))
	}
	if capture.stored[0].ReplyClass != replyclassify.ClassPositive {
		t.Fatalf("stored reply class = %q, want positive", capture.stored[0].ReplyClass)
	}
}

// TestPollAutoReplyIsAlsoStoredInTheInbox proves inbox storage fires for EVERY
// reply class, not just the ones that stop an enrollment: an out-of-office
// message (same fixture as TestPollOutOfOfficeRecordsClassWithoutStopping,
// which is tagged via RecordReplyClass but keeps the enrollment active) is
// still stored.
func TestPollAutoReplyIsAlsoStoredInTheInbox(t *testing.T) {
	core := &stubCore{
		job: coreapi.InboxPollJob{UIDValidity: 5, LastSeenUID: 10},
		sendRefs: map[string]coreapi.SendRef{"<root@x>": {
			SendID: "s1", EnrollmentID: "e1", ContactEmail: "a@b.io",
			MailboxID: "mb1", CampaignID: "camp1", ContactID: "contact1", MessageID: "<root@x>",
		}},
	}
	capture := &fakeInboxCapture{}
	reader := &fakeReader{
		uidValidity: 5, uidNext: 12,
		msgs: []mail.InboundMessage{inboundMsg(t, 11, replyWith("Out of Office", "I am away until Monday.", "<root@x>"))},
	}
	if err := runPoll(t, coreWithInboxCapture{stubCore: core, fakeInboxCapture: capture}, reader); err != nil {
		t.Fatal(err)
	}
	if len(capture.stored) != 1 {
		t.Fatalf("expected 1 stored message, got %d", len(capture.stored))
	}
	if capture.stored[0].ReplyClass != replyclassify.ClassOutOfOffice {
		t.Fatalf("stored reply class = %q, want out_of_office", capture.stored[0].ReplyClass)
	}
}

// TestPollUnmatchedMessageStoresNothing proves a message that never matched a
// send (same fixture as TestPollNoMatchIsIgnoredButCursorAdvances) is never
// reachable by the store call — there is no s (matched send) to build an
// InboxMessageInput from.
func TestPollUnmatchedMessageStoresNothing(t *testing.T) {
	core := &stubCore{job: coreapi.InboxPollJob{UIDValidity: 5, LastSeenUID: 10}}
	capture := &fakeInboxCapture{}
	reader := &fakeReader{
		uidValidity: 5, uidNext: 12,
		msgs: []mail.InboundMessage{inboundMsg(t, 11, replyFixture("<unknown@x>"))},
	}
	if err := runPoll(t, coreWithInboxCapture{stubCore: core, fakeInboxCapture: capture}, reader); err != nil {
		t.Fatal(err)
	}
	if len(capture.stored) != 0 {
		t.Fatalf("expected nothing stored for an unmatched message, got %d", len(capture.stored))
	}
}

// TestPollWithoutAnInboxCaptureClientStillClassifiesNormally proves the
// feature-detection type assertion in processMessage never panics when core
// does NOT implement coreapi.InboxCaptureClient (plain *stubCore, same as
// every pre-existing test in this file) — same fixture and assertions as
// TestPollPositiveReplyMarksRepliedPositive, which must still hold unchanged.
func TestPollWithoutAnInboxCaptureClientStillClassifiesNormally(t *testing.T) {
	core := &stubCore{
		job:      coreapi.InboxPollJob{UIDValidity: 5, LastSeenUID: 10},
		sendRefs: map[string]coreapi.SendRef{"<root@x>": {SendID: "s1", EnrollmentID: "e1", ContactEmail: "a@b.io"}},
	}
	reader := &fakeReader{
		uidValidity: 5, uidNext: 12,
		msgs: []mail.InboundMessage{inboundMsg(t, 11, replyWith("Re: Hello", "Sounds great, let's chat this week.", "<root@x>"))},
	}
	if err := runPoll(t, core, reader); err != nil {
		t.Fatal(err)
	}
	if len(core.replied) != 1 || core.replied[0] != "e1" || core.repliedClass[0] != replyclassify.ClassPositive {
		t.Fatalf("expected MarkReplied(e1, positive), got ids=%v classes=%v", core.replied, core.repliedClass)
	}
	if len(core.unsubscribed) != 0 || len(core.recorded) != 0 {
		t.Fatalf("a positive reply must only MarkReplied, got unsub=%v recorded=%v", core.unsubscribed, core.recorded)
	}
	if len(core.captured) != 1 || core.captured[0].SendID != "s1" ||
		core.captured[0].EnrollmentID != "e1" || core.captured[0].ReplyClass != replyclassify.ClassPositive {
		t.Fatalf("positive reply was not captured for CRM: %+v", core.captured)
	}
}
