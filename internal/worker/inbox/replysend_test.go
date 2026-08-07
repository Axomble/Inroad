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

	jobCalls        [][2]string // {threadID, workspaceID}
	transportCalls  [][2]string // {workspaceID, mailboxID}
	suppressedCalls [][2]string // {workspaceID, to}
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

func replySendTask(t *testing.T, p queue.InboxReplySendPayload) *asynq.Task {
	t.Helper()
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return asynq.NewTask(queue.TaskInboxReplySend, b)
}

func defaultReplyPayload() queue.InboxReplySendPayload {
	return queue.InboxReplySendPayload{ThreadID: "thread-1", BodyText: "thanks for reaching out", WorkspaceID: "ws-1"}
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
	task := asynq.NewTask(queue.TaskInboxReplySend, []byte("not json"))
	if err := ReplySendHandler(&stubReplyCore{}, &stubMailer{})(context.Background(), task); err == nil {
		t.Fatal("err = nil, want an error for a malformed payload")
	}
}
