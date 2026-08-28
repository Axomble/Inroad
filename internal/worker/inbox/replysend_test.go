package inbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/queue"
)

// stubReplyCore is the mocked ReplyCore: it never touches a Keyring, an
// OAuth token, or Postgres — it just records the ids it was asked about and
// hands back fixed content/transport.
type stubReplyCore struct {
	job    coreapi.InboxReplyJob
	jobErr error

	transport    coreapi.SenderTransport
	transportErr error

	suppressed    bool
	suppressedErr error

	recordErr error

	// claimTaken models the claim row's real existence: false (the default,
	// so every test that never sets it exercises a fresh claim unchanged)
	// means ClaimInboxReply succeeds and flips it to true; a successful
	// ReleaseInboxReply flips it back to false, so a SECOND handler
	// invocation in the same test (simulating a retry) can re-claim, mirroring
	// the real idempotency_keys insert/delete pair.
	claimTaken bool
	claimErr   error
	releaseErr error

	jobCalls        [][2]string // {threadID, workspaceID}
	transportCalls  [][2]string // {workspaceID, mailboxID}
	suppressedCalls [][2]string // {workspaceID, to}
	claimCalls      [][2]string // {workspaceID, taskID}
	releaseCalls    [][2]string // {workspaceID, taskID}
	recordCalls     []coreapi.RecordInboxReplyInput
}

func (s *stubReplyCore) GetInboxReplyJob(_ context.Context, threadID, workspaceID string) (coreapi.InboxReplyJob, error) {
	s.jobCalls = append(s.jobCalls, [2]string{threadID, workspaceID})
	return s.job, s.jobErr
}

func (s *stubReplyCore) IsSuppressed(_ context.Context, workspaceID, to string) (bool, error) {
	s.suppressedCalls = append(s.suppressedCalls, [2]string{workspaceID, to})
	return s.suppressed, s.suppressedErr
}

func (s *stubReplyCore) ResolveSenderTransport(_ context.Context, workspaceID, mailboxID string) (coreapi.SenderTransport, error) {
	s.transportCalls = append(s.transportCalls, [2]string{workspaceID, mailboxID})
	return s.transport, s.transportErr
}

func (s *stubReplyCore) RecordInboxReply(_ context.Context, in coreapi.RecordInboxReplyInput) error {
	s.recordCalls = append(s.recordCalls, in)
	return s.recordErr
}

func (s *stubReplyCore) ClaimInboxReply(_ context.Context, workspaceID, taskID string) (bool, error) {
	s.claimCalls = append(s.claimCalls, [2]string{workspaceID, taskID})
	if s.claimErr != nil {
		return false, s.claimErr
	}
	if s.claimTaken {
		return false, nil
	}
	s.claimTaken = true
	return true, nil
}

func (s *stubReplyCore) ReleaseInboxReply(_ context.Context, workspaceID, taskID string) error {
	s.releaseCalls = append(s.releaseCalls, [2]string{workspaceID, taskID})
	if s.releaseErr != nil {
		return s.releaseErr
	}
	s.claimTaken = false
	return nil
}

// stubMailer is the mocked mail sender seam: it never dials out, it just
// records the last message it was asked to send.
type stubMailer struct {
	calls int
	tj    mail.OutboundJob
	msg   mail.Message
	err   error
}

func (m *stubMailer) Send(_ context.Context, tj mail.OutboundJob, msg mail.Message) (string, error) {
	m.calls++
	m.tj, m.msg = tj, msg
	if m.err != nil {
		return "", m.err
	}
	return "reply-message-id", nil
}

// legacyReplyTask wraps raw bytes in an inbox:reply_send task. Every task type
// reference in this file goes through it, so the deprecation is acknowledged
// once: the whole file tests a drain path, and naming the retired task type is
// the only thing it could possibly be doing.
//
//nolint:staticcheck // SA1019: the deprecated task type is this file's subject.
func legacyReplyTask(payload []byte) *asynq.Task {
	return asynq.NewTask(queue.TaskInboxReplySend, payload)
}

//nolint:staticcheck // SA1019: same — the deprecated payload is the subject.
func replySendTask(t *testing.T, p queue.InboxReplySendPayload) *asynq.Task {
	t.Helper()
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return legacyReplyTask(b)
}

//nolint:staticcheck // SA1019: same — the deprecated payload is the subject.
func defaultReplyPayload() queue.InboxReplySendPayload {
	return queue.InboxReplySendPayload{
		ThreadID: "thread-1", BodyText: "thanks for reaching out", WorkspaceID: "ws-1", TaskID: "task-1",
	}
}

func defaultReplyJob() coreapi.InboxReplyJob {
	return coreapi.InboxReplyJob{
		MailboxID: "mbox-1", Subject: "Interested?", ToEmail: "lead@x.test",
		InReplyTo: "msg-1", References: "msg-0 msg-1",
	}
}

func TestReSubjectPrefixesOnce(t *testing.T) {
	cases := map[string]string{
		"Interested?":     "Re: Interested?",
		"Re: Interested?": "Re: Interested?",
		"re: interested?": "re: interested?",
		"RE: Interested?": "RE: Interested?",
		"":                "Re: ",
	}
	for in, want := range cases {
		if got := reSubject(in); got != want {
			t.Errorf("reSubject(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestReplySendHandlerHappyPathSendsAndRecords(t *testing.T) {
	core := &stubReplyCore{
		job: defaultReplyJob(),
		transport: coreapi.SenderTransport{
			FromEmail: "me@x.test", FromName: "Me", Provider: "smtp",
			SMTPHost: "smtp.x.test", SMTPPort: 587, SMTPUsername: "u", SMTPPassword: []byte("secret"),
		},
	}
	mailer := &stubMailer{}

	if err := ReplySendHandler(core, mailer)(context.Background(), replySendTask(t, defaultReplyPayload())); err != nil {
		t.Fatalf("ReplySendHandler: %v", err)
	}
	if mailer.calls != 1 {
		t.Fatalf("mailer calls = %d, want 1", mailer.calls)
	}
	if mailer.msg.To != "lead@x.test" || mailer.msg.FromEmail != "me@x.test" {
		t.Errorf("msg = %+v, want the job's recipient and the resolved sender identity", mailer.msg)
	}
	if mailer.msg.Subject != "Re: Interested?" {
		t.Errorf("subject = %q, want an idempotent Re: prefix", mailer.msg.Subject)
	}
	if mailer.msg.BodyText != "thanks for reaching out" {
		t.Errorf("body_text = %q, want the payload's body", mailer.msg.BodyText)
	}
	if mailer.msg.InReplyTo != "msg-1" || mailer.msg.References != "msg-0 msg-1" {
		t.Errorf("threading headers = %+v, want the job's InReplyTo/References", mailer.msg)
	}
	if mailer.msg.ListUnsubscribe != "" {
		t.Error("a conversational reply must carry no List-Unsubscribe header")
	}
	if len(core.recordCalls) != 1 {
		t.Fatalf("record calls = %d, want 1", len(core.recordCalls))
	}
	rec := core.recordCalls[0]
	if rec.MessageID != "reply-message-id" || rec.ThreadID != "thread-1" || rec.WorkspaceID != "ws-1" ||
		rec.ToEmail != "lead@x.test" || rec.Subject != "Re: Interested?" || rec.BodyText != "thanks for reaching out" {
		t.Errorf("recorded reply = %+v, unexpected fields", rec)
	}
}

func TestReplySendHandlerPassesWorkspacePinnedIDsToCore(t *testing.T) {
	core := &stubReplyCore{job: defaultReplyJob(), transport: coreapi.SenderTransport{Provider: "smtp"}}
	mailer := &stubMailer{}

	if err := ReplySendHandler(core, mailer)(context.Background(), replySendTask(t, defaultReplyPayload())); err != nil {
		t.Fatalf("ReplySendHandler: %v", err)
	}
	if len(core.jobCalls) != 1 || core.jobCalls[0] != [2]string{"thread-1", "ws-1"} {
		t.Errorf("job calls = %+v, want [{thread-1 ws-1}]", core.jobCalls)
	}
	if len(core.claimCalls) != 1 || core.claimCalls[0] != [2]string{"ws-1", "task-1"} {
		t.Errorf("claim calls = %+v, want [{ws-1 task-1}]", core.claimCalls)
	}
	if len(core.transportCalls) != 1 || core.transportCalls[0] != [2]string{"ws-1", "mbox-1"} {
		t.Errorf("transport calls = %+v, want [{ws-1 mbox-1}]", core.transportCalls)
	}
}

func TestReplySendHandlerNoInboundMessageIsDroppedNotRetried(t *testing.T) {
	core := &stubReplyCore{jobErr: coreapi.ErrInboxNoInbound}
	mailer := &stubMailer{}

	if err := ReplySendHandler(core, mailer)(context.Background(), replySendTask(t, defaultReplyPayload())); err != nil {
		t.Fatalf("ReplySendHandler: %v, want nil (permanent — log and drop, never retry)", err)
	}
	if mailer.calls != 0 {
		t.Error("a thread with no inbound message must not send anything")
	}
}

func TestReplySendHandlerPropagatesJobLoadError(t *testing.T) {
	boom := errors.New("db down")
	core := &stubReplyCore{jobErr: boom}
	mailer := &stubMailer{}

	err := ReplySendHandler(core, mailer)(context.Background(), replySendTask(t, defaultReplyPayload()))
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the propagated job-load error", err)
	}
	if mailer.calls != 0 {
		t.Error("a job-load failure must not send anything")
	}
}

// --- Defense-in-depth suppression re-check ----------------------------------

func TestReplySendHandlerSkipsASuppressedRecipientWithoutSending(t *testing.T) {
	core := &stubReplyCore{job: defaultReplyJob(), suppressed: true, transport: coreapi.SenderTransport{Provider: "smtp"}}
	mailer := &stubMailer{}

	if err := ReplySendHandler(core, mailer)(context.Background(), replySendTask(t, defaultReplyPayload())); err != nil {
		t.Fatalf("ReplySendHandler: %v, want nil (a suppressed recipient is skipped, not failed)", err)
	}
	if mailer.calls != 0 {
		t.Error("a suppressed recipient must not be sent to")
	}
	if len(core.transportCalls) != 0 {
		t.Error("a suppressed recipient must not resolve (decrypt) a sender transport")
	}
	if len(core.suppressedCalls) != 1 || core.suppressedCalls[0] != [2]string{"ws-1", "lead@x.test"} {
		t.Errorf("suppression check calls = %+v, want [{ws-1 lead@x.test}]", core.suppressedCalls)
	}
}

func TestReplySendHandlerPropagatesTheSuppressionCheckError(t *testing.T) {
	boom := errors.New("db unavailable")
	core := &stubReplyCore{job: defaultReplyJob(), suppressedErr: boom}
	mailer := &stubMailer{}

	err := ReplySendHandler(core, mailer)(context.Background(), replySendTask(t, defaultReplyPayload()))
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the propagated suppression-check error", err)
	}
	if mailer.calls != 0 {
		t.Error("a suppression-check failure must not send anything")
	}
}

func TestReplySendHandlerPropagatesTheSendError(t *testing.T) {
	boom := errors.New("smtp: connection refused")
	core := &stubReplyCore{job: defaultReplyJob(), transport: coreapi.SenderTransport{Provider: "smtp"}}
	mailer := &stubMailer{err: boom}

	err := ReplySendHandler(core, mailer)(context.Background(), replySendTask(t, defaultReplyPayload()))
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the propagated send error", err)
	}
	if len(core.recordCalls) != 0 {
		t.Error("a failed send must not be recorded")
	}
}

// --- Claim-before-send ------------------------------------------------------
//
// A fresh claim is exercised by every happy-path test above (stubReplyCore's
// claimTaken defaults to false). These tests cover the rest: a duplicate
// claim skips without dialing, a claim-check failure propagates, a transient
// send failure releases the claim so a retry can re-claim, and a
// release failure still returns the original send error (fail-safe: drop,
// never double).

func TestReplySendHandlerSkipsWhenAlreadyClaimed(t *testing.T) {
	core := &stubReplyCore{job: defaultReplyJob(), transport: coreapi.SenderTransport{Provider: "smtp"}, claimTaken: true}
	mailer := &stubMailer{}

	if err := ReplySendHandler(core, mailer)(context.Background(), replySendTask(t, defaultReplyPayload())); err != nil {
		t.Fatalf("ReplySendHandler: %v, want nil (already claimed by a prior attempt — skip, don't retry)", err)
	}
	if mailer.calls != 0 {
		t.Error("an already-claimed task must not dial the provider")
	}
	if len(core.transportCalls) != 0 {
		t.Error("an already-claimed task must not resolve (decrypt) a sender transport")
	}
}

func TestReplySendHandlerPropagatesClaimError(t *testing.T) {
	boom := errors.New("db down")
	core := &stubReplyCore{job: defaultReplyJob(), claimErr: boom}
	mailer := &stubMailer{}

	err := ReplySendHandler(core, mailer)(context.Background(), replySendTask(t, defaultReplyPayload()))
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the propagated claim error", err)
	}
	if mailer.calls != 0 {
		t.Error("a claim-check failure must not send anything")
	}
}

// A transient send failure must release the claim BEFORE returning err, so a
// retry of the SAME task (same task id) can re-claim rather than seeing its
// own abandoned claim and skipping forever.
func TestReplySendHandlerReleasesClaimOnTransientFailureThenRetryReClaims(t *testing.T) {
	core := &stubReplyCore{job: defaultReplyJob(), transport: coreapi.SenderTransport{Provider: "smtp"}}
	boom := errors.New("smtp: connection refused")
	mailer := &stubMailer{err: boom}

	if err := ReplySendHandler(core, mailer)(context.Background(), replySendTask(t, defaultReplyPayload())); !errors.Is(err, boom) {
		t.Fatalf("first attempt err = %v, want %v", err, boom)
	}
	if len(core.releaseCalls) != 1 {
		t.Fatalf("release calls = %d, want 1 after the transient send failure", len(core.releaseCalls))
	}

	// The retry: asynq redelivers the identical task/payload, now succeeding.
	mailer.err = nil
	if err := ReplySendHandler(core, mailer)(context.Background(), replySendTask(t, defaultReplyPayload())); err != nil {
		t.Fatalf("retry: %v, want nil (the release must have let the retry re-claim)", err)
	}
	if len(core.claimCalls) != 2 {
		t.Fatalf("claim calls = %d, want 2 (the original attempt + the retry)", len(core.claimCalls))
	}
	if mailer.calls != 2 {
		t.Fatalf("mailer calls = %d, want 2 (the failed attempt + the successful retry)", mailer.calls)
	}
}

// If the release itself fails, the send error must still be returned (fail
// safe: a stuck claim only ever drops a retry, never risks a double send).
func TestReplySendHandlerSendFailureReturnsErrEvenIfReleaseFails(t *testing.T) {
	core := &stubReplyCore{
		job: defaultReplyJob(), transport: coreapi.SenderTransport{Provider: "smtp"},
		releaseErr: errors.New("db down"),
	}
	boom := errors.New("smtp: connection refused")
	mailer := &stubMailer{err: boom}

	err := ReplySendHandler(core, mailer)(context.Background(), replySendTask(t, defaultReplyPayload()))
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the propagated send error even though the release failed", err)
	}
}

// A transport-resolve failure happens AFTER the claim but BEFORE anything is
// dialed, so it must release the claim exactly like a send failure does —
// otherwise a transient keyring/DB blip here would permanently (and
// silently) drop every retry rather than letting one eventually succeed.
func TestReplySendHandlerReleasesClaimOnTransportResolveFailure(t *testing.T) {
	boom := errors.New("keyring unavailable")
	core := &stubReplyCore{job: defaultReplyJob(), transportErr: boom}
	mailer := &stubMailer{}

	err := ReplySendHandler(core, mailer)(context.Background(), replySendTask(t, defaultReplyPayload()))
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the propagated transport-resolve error", err)
	}
	if len(core.releaseCalls) != 1 {
		t.Fatalf("release calls = %d, want 1", len(core.releaseCalls))
	}
	if mailer.calls != 0 {
		t.Error("a transport-resolve failure must not send anything")
	}
}

// TestReplySendHandlerNeverRetriesAfterADeliveredSend proves the handler
// swallows (logs, does not return) a post-send RecordInboxReply failure: were
// it returned, asynq would retry the WHOLE handler, including sender.Send,
// and double-send to the recipient.
func TestReplySendHandlerNeverRetriesAfterADeliveredSend(t *testing.T) {
	core := &stubReplyCore{job: defaultReplyJob(), transport: coreapi.SenderTransport{Provider: "smtp"}, recordErr: errors.New("db down")}
	mailer := &stubMailer{}

	if err := ReplySendHandler(core, mailer)(context.Background(), replySendTask(t, defaultReplyPayload())); err != nil {
		t.Fatalf("ReplySendHandler: %v, want nil (the reply was delivered; a bookkeeping failure must not trigger a retry)", err)
	}
	if mailer.calls != 1 {
		t.Fatalf("mailer calls = %d, want exactly 1 (no retry-induced double send)", mailer.calls)
	}
}

func TestReplySendHandlerZeroizesTheCredentialAfterSend(t *testing.T) {
	password := []byte("supersecret")
	token := []byte("oauth-token")
	core := &stubReplyCore{
		job:       defaultReplyJob(),
		transport: coreapi.SenderTransport{Provider: "smtp", SMTPPassword: password, AccessToken: token},
	}
	mailer := &stubMailer{}

	if err := ReplySendHandler(core, mailer)(context.Background(), replySendTask(t, defaultReplyPayload())); err != nil {
		t.Fatalf("ReplySendHandler: %v", err)
	}
	for _, b := range password {
		if b != 0 {
			t.Fatalf("SMTPPassword not zeroized: %v", password)
		}
	}
	for _, b := range token {
		if b != 0 {
			t.Fatalf("AccessToken not zeroized: %v", token)
		}
	}
	if mailer.tj.Password != "supersecret" {
		t.Errorf("mailer's password copy = %q, want it unaffected by the later zeroize", mailer.tj.Password)
	}
}

func TestReplySendHandlerRejectsMalformedPayload(t *testing.T) {
	task := legacyReplyTask([]byte("not json"))
	if err := ReplySendHandler(&stubReplyCore{}, &stubMailer{})(context.Background(), task); err == nil {
		t.Fatal("err = nil, want an error for a malformed payload")
	}
}

// --- Drain compatibility ----------------------------------------------------

// legacyReplySendJSON is the payload shape as it was ENQUEUED, written out by
// hand rather than marshalled from queue.InboxReplySendPayload.
//
// That is the whole point of it. This handler exists only to finish tasks that
// were already sitting in Redis when the body stopped travelling in a payload;
// the struct it unmarshals into is deprecated and is deleted in the release
// after this one. A literal survives that deletion, so whoever removes the type
// has to face this test and confirm the queue has actually drained — whereas a
// test that marshalled the struct would vanish silently along with it, taking
// the only proof of in-flight compatibility with it.
const legacyReplySendJSON = `{
	"thread_id": "thread-1",
	"body_text": "thanks for reaching out",
	"workspace_id": "ws-1",
	"task_id": "task-1"
}`

// An old-shape task must still send from its own payload. Nothing produces one
// any more, but one already in the queue has no row to read the body from — so
// changing this handler to load from inbox_pending_replies would silently drop
// every reply in flight at deploy time.
func TestReplySendHandlerStillSendsAnOldShapePayloadFromItsOwnBody(t *testing.T) {
	core := &stubReplyCore{
		job:       defaultReplyJob(),
		transport: coreapi.SenderTransport{Provider: "smtp", FromEmail: "me@x.test"},
	}
	mailer := &stubMailer{}

	task := legacyReplyTask([]byte(legacyReplySendJSON))
	if err := ReplySendHandler(core, mailer)(context.Background(), task); err != nil {
		t.Fatalf("ReplySendHandler: %v", err)
	}
	if mailer.calls != 1 {
		t.Fatalf("mailer calls = %d, want 1 — an in-flight legacy task must still be delivered", mailer.calls)
	}
	if mailer.msg.BodyText != "thanks for reaching out" {
		t.Errorf("body_text = %q, want the legacy payload's own body: there is no row to read it from",
			mailer.msg.BodyText)
	}
	if mailer.msg.To != "lead@x.test" {
		t.Errorf("to = %q, want the job's recipient", mailer.msg.To)
	}
	if len(core.claimCalls) != 1 || core.claimCalls[0] != [2]string{"ws-1", "task-1"} {
		t.Errorf("claim calls = %+v, want [{ws-1 task-1}] read from the legacy payload", core.claimCalls)
	}
}

// The drain's own signal. An operator deletes this handler when it stops firing,
// so it has to say it fired — with IDS ONLY. The payload is the operator's
// correspondence; logging it here would recreate, in the log sink, exactly the
// disclosure moving the body out of the payload closed.
func TestReplySendHandlerLogsTheDrainWithIDsOnlyAndNeverTheBody(t *testing.T) {
	restore := slog.Default()
	var logs bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(restore) })

	core := &stubReplyCore{job: defaultReplyJob(), transport: coreapi.SenderTransport{Provider: "smtp"}}
	task := legacyReplyTask([]byte(legacyReplySendJSON))
	if err := ReplySendHandler(core, &stubMailer{})(context.Background(), task); err != nil {
		t.Fatalf("ReplySendHandler: %v", err)
	}

	out := logs.String()
	if !strings.Contains(out, "inbox_reply_send_legacy_drain") {
		t.Fatalf("the drain was not logged, so nothing tells an operator this handler is still "+
			"needed: %s", out)
	}
	if !strings.Contains(out, "thread-1") || !strings.Contains(out, "ws-1") {
		t.Errorf("the drain log names no ids, so it cannot be traced: %s", out)
	}
	if strings.Contains(out, "thanks for reaching out") {
		t.Errorf("the reply body was written to the log: %s", out)
	}
}
