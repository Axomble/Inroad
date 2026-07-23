package inbox

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/queue"
)

// fakeReader is a test double for mail.InboxReader.
type fakeReader struct {
	uidValidity uint32
	uidNext     uint32
	stateErr    error

	msgs     []mail.InboundMessage
	fetchErr error

	fetchCalled bool
	sinceUID    uint32
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

	replied []string
	bounced []bouncedCall

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

func (f *fakeGmailReader) Fetch(_ context.Context, _ string, sinceHistoryID string, _ int) ([]mail.InboundMessage, string, error) {
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

func (s *stubCore) MarkReplied(_ context.Context, enrollmentID, _ string) error {
	s.replied = append(s.replied, enrollmentID)
	return nil
}

func (s *stubCore) MarkBounced(_ context.Context, enrollmentID, _, email string, hard bool) error {
	s.bounced = append(s.bounced, bouncedCall{enrollmentID, email, hard})
	return nil
}

func (s *stubCore) ListActiveMailboxes(context.Context) ([]coreapi.MailboxRef, error) {
	return s.mailboxes, s.listErr
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

func autoReplyFixture(inReplyTo string) string {
	return "From: bot@example.com\nTo: bob@example.com\nSubject: Out of office\nIn-Reply-To: " +
		inReplyTo + "\nAuto-Submitted: auto-replied\n\nI'm out of the office.\n"
}

func runPoll(t *testing.T, core coreapi.Client, reader mail.InboxReader) error {
	t.Helper()
	// nil Gmail reader: every runPoll test drives the smtp/IMAP path (job.Provider
	// defaults to "", so the gmail branch is never taken and the reader is unused).
	return PollHandler(core, reader, nil)(context.Background(), pollTask(t))
}

func runGmailPoll(t *testing.T, core coreapi.Client, gmail GmailFetcher) error {
	t.Helper()
	return PollHandler(core, nil, gmail)(context.Background(), pollTask(t))
}

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
	if len(core.replied) != 0 {
		t.Fatalf("an auto-reply must not be treated as an engaged reply, got %v", core.replied)
	}
	if !core.cursorSet || core.cursorUID != 11 {
		t.Fatal("cursor must still advance past a skipped message")
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
