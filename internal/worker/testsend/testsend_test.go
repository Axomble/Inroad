package testsend

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

// stubCore is the mocked testsend.Core: it never touches a Keyring, an OAuth
// token, or Postgres -- it just records the ids it was asked about and hands
// back fixed content/transport.
type stubCore struct {
	content      coreapi.TestSendContent
	contentErr   error
	transport    coreapi.SenderTransport
	transportErr error
	// suppressed/suppressedErr back IsSuppressed; suppressed defaults to
	// false (not suppressed) so every existing test -- which never sets it --
	// keeps exercising the full send path unchanged.
	suppressed    bool
	suppressedErr error

	contentCalls    [][3]string // {workspaceID, campaignID, stepID}
	transportCalls  [][2]string // {workspaceID, mailboxID}
	suppressedCalls [][2]string // {workspaceID, to}
}

func (s *stubCore) GetTestSendContent(_ context.Context, workspaceID, campaignID, stepID string) (coreapi.TestSendContent, error) {
	s.contentCalls = append(s.contentCalls, [3]string{workspaceID, campaignID, stepID})
	return s.content, s.contentErr
}

func (s *stubCore) ResolveSenderTransport(_ context.Context, workspaceID, mailboxID string) (coreapi.SenderTransport, error) {
	s.transportCalls = append(s.transportCalls, [2]string{workspaceID, mailboxID})
	return s.transport, s.transportErr
}

func (s *stubCore) IsSuppressed(_ context.Context, workspaceID, to string) (bool, error) {
	s.suppressedCalls = append(s.suppressedCalls, [2]string{workspaceID, to})
	return s.suppressed, s.suppressedErr
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
	return "test-message-id", nil
}

func testSendTask(t *testing.T, p queue.TestSendPayload) *asynq.Task {
	t.Helper()
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return asynq.NewTask(queue.TaskTestSend, b)
}

func defaultPayload() queue.TestSendPayload {
	return queue.TestSendPayload{
		CampaignID: "camp-1", StepID: "step-1", MailboxID: "mbox-1",
		To: "preview@example.com", WorkspaceID: "ws-1",
	}
}

// TestHandlerEscapesHTMLButLeavesTextAndSubjectRaw is the parity finding:
// personalize.HTML html.EscapeString's every substituted value, personalize.
// Text does not -- the SAME rule production sends apply (personalize.go).
func TestHandlerEscapesHTMLButLeavesTextAndSubjectRaw(t *testing.T) {
	core := &stubCore{
		content: coreapi.TestSendContent{
			Subject: "Hi {{first_name}}", BodyText: "Hi {{first_name}}", BodyHTML: "<p>Hi {{first_name}}</p>",
			FirstName: "<b>Ada</b>", Company: "Ex & Co",
		},
		transport: coreapi.SenderTransport{FromEmail: "a@x.test", FromName: "Sender", Provider: "smtp"},
	}
	mailer := &stubMailer{}

	if err := Handler(core, mailer)(context.Background(), testSendTask(t, defaultPayload())); err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if mailer.calls != 1 {
		t.Fatalf("mailer calls = %d, want 1", mailer.calls)
	}
	if mailer.msg.Subject != "[Test] Hi <b>Ada</b>" {
		t.Errorf("subject = %q, want the [Test] prefix and RAW (unescaped) first_name", mailer.msg.Subject)
	}
	if mailer.msg.BodyText != "Hi <b>Ada</b>" {
		t.Errorf("body_text = %q, want RAW (unescaped) first_name", mailer.msg.BodyText)
	}
	if mailer.msg.BodyHTML != "<p>Hi &lt;b&gt;Ada&lt;/b&gt;</p>" {
		t.Errorf("body_html = %q, want first_name HTML-escaped", mailer.msg.BodyHTML)
	}
}

// TestHandlerFallsBackToSyntheticVarsWhenTheLoaderReportsThem proves the
// worker renders whatever GetTestSendContent returns -- the fallback
// substitution policy lives in the loader (coreapi), not here.
func TestHandlerFallsBackToSyntheticVarsWhenTheLoaderReportsThem(t *testing.T) {
	core := &stubCore{
		content:   coreapi.TestSendContent{Subject: "Hi {{first_name}} from {{company}}", FirstName: "Alex", Company: "Acme"},
		transport: coreapi.SenderTransport{Provider: "smtp"},
	}
	mailer := &stubMailer{}

	if err := Handler(core, mailer)(context.Background(), testSendTask(t, defaultPayload())); err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if mailer.msg.Subject != "[Test] Hi Alex from Acme" {
		t.Errorf("subject = %q", mailer.msg.Subject)
	}
}

func TestHandlerSendsToTheRequestedRecipientWithNoTrackingOrThreadingHeaders(t *testing.T) {
	core := &stubCore{
		content: coreapi.TestSendContent{Subject: "S", BodyText: "T", BodyHTML: "<p>H</p>", FirstName: "Alex", Company: "Acme"},
		transport: coreapi.SenderTransport{
			FromEmail: "from@x.test", FromName: "From Name", Provider: "smtp",
			SMTPHost: "smtp.x.test", SMTPPort: 587, SMTPUsername: "u", SMTPPassword: []byte("secret"),
		},
	}
	mailer := &stubMailer{}

	if err := Handler(core, mailer)(context.Background(), testSendTask(t, defaultPayload())); err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if mailer.msg.To != "preview@example.com" {
		t.Errorf("To = %q, want the requested preview recipient", mailer.msg.To)
	}
	if mailer.msg.FromEmail != "from@x.test" || mailer.msg.FromName != "From Name" {
		t.Errorf("from = %q/%q, want the resolved sender identity", mailer.msg.FromEmail, mailer.msg.FromName)
	}
	if mailer.msg.ListUnsubscribe != "" || mailer.msg.InReplyTo != "" || mailer.msg.References != "" {
		t.Errorf("msg = %+v, want no unsubscribe/threading headers -- a test-send is never subject to that machinery", mailer.msg)
	}
	if mailer.tj.Provider != "smtp" || mailer.tj.Host != "smtp.x.test" || mailer.tj.Port != 587 ||
		mailer.tj.Username != "u" || mailer.tj.Password != "secret" {
		t.Errorf("outbound job = %+v, want the resolved transport", mailer.tj)
	}
}

func TestHandlerPassesWorkspacePinnedIDsToCore(t *testing.T) {
	core := &stubCore{transport: coreapi.SenderTransport{Provider: "smtp"}}
	mailer := &stubMailer{}

	if err := Handler(core, mailer)(context.Background(), testSendTask(t, defaultPayload())); err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if len(core.contentCalls) != 1 || core.contentCalls[0] != [3]string{"ws-1", "camp-1", "step-1"} {
		t.Errorf("content calls = %+v, want [{ws-1 camp-1 step-1}]", core.contentCalls)
	}
	if len(core.transportCalls) != 1 || core.transportCalls[0] != [2]string{"ws-1", "mbox-1"} {
		t.Errorf("transport calls = %+v, want [{ws-1 mbox-1}]", core.transportCalls)
	}
}

func TestHandlerPropagatesContentLoadError(t *testing.T) {
	boom := errors.New("db down")
	core := &stubCore{contentErr: boom}
	mailer := &stubMailer{}

	err := Handler(core, mailer)(context.Background(), testSendTask(t, defaultPayload()))
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the propagated content-load error", err)
	}
	if mailer.calls != 0 {
		t.Error("a content-load failure must not send anything")
	}
}

func TestHandlerPropagatesTransportResolveError(t *testing.T) {
	boom := errors.New("keyring unavailable")
	core := &stubCore{transportErr: boom}
	mailer := &stubMailer{}

	err := Handler(core, mailer)(context.Background(), testSendTask(t, defaultPayload()))
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the propagated transport-resolve error", err)
	}
	if mailer.calls != 0 {
		t.Error("a transport-resolve failure must not send anything")
	}
}

func TestHandlerPropagatesTheSendError(t *testing.T) {
	boom := errors.New("smtp: connection refused")
	core := &stubCore{transport: coreapi.SenderTransport{Provider: "smtp"}}
	mailer := &stubMailer{err: boom}

	err := Handler(core, mailer)(context.Background(), testSendTask(t, defaultPayload()))
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the propagated send error", err)
	}
}

// TestHandlerZeroizesTheCredentialAfterSend proves the decrypted secret is
// wiped after use, mirroring internal/worker/sender's identical defer.
func TestHandlerZeroizesTheCredentialAfterSend(t *testing.T) {
	password := []byte("supersecret")
	token := []byte("oauth-token")
	core := &stubCore{transport: coreapi.SenderTransport{Provider: "smtp", SMTPPassword: password, AccessToken: token}}
	mailer := &stubMailer{}

	if err := Handler(core, mailer)(context.Background(), testSendTask(t, defaultPayload())); err != nil {
		t.Fatalf("Handler: %v", err)
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
	// The Mailer's own copy (a string, taken before the deferred zeroize runs)
	// must be unaffected -- the same "string copy is immutable, only our buffer
	// is wiped" rationale as internal/worker/sender.Handler.
	if mailer.tj.Password != "supersecret" {
		t.Errorf("mailer's password copy = %q, want it unaffected by the later zeroize", mailer.tj.Password)
	}
}

// TestHandlerResolvesSpintaxByStepID proves the preview resolves
// "{option|option}" spin syntax through the same Expand-then-personalize
// pipeline the real send path uses (internal/worker/sequence.advance),
// keyed on the step id -- the stable id available here, since a preview has
// no sends row and therefore no SendID. Two previews of the SAME step must
// pick the SAME option: that determinism is the point of seeding on an id
// rather than drawing fresh randomness per call.
func TestHandlerResolvesSpintaxByStepID(t *testing.T) {
	core := &stubCore{
		content: coreapi.TestSendContent{
			Subject: "Hi {{first_name}}, {this|that}", BodyText: "T", BodyHTML: "<p>H</p>",
			FirstName: "Alex", Company: "Acme",
		},
		transport: coreapi.SenderTransport{Provider: "smtp"},
	}
	mailer1, mailer2 := &stubMailer{}, &stubMailer{}

	if err := Handler(core, mailer1)(context.Background(), testSendTask(t, defaultPayload())); err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if err := Handler(core, mailer2)(context.Background(), testSendTask(t, defaultPayload())); err != nil {
		t.Fatalf("Handler: %v", err)
	}

	if mailer1.msg.Subject != mailer2.msg.Subject {
		t.Fatalf("spin pick drifted across two previews of the same step: %q vs %q", mailer1.msg.Subject, mailer2.msg.Subject)
	}
	if mailer1.msg.Subject != "[Test] Hi Alex, this" && mailer1.msg.Subject != "[Test] Hi Alex, that" {
		t.Fatalf("subject = %q, want the spin group resolved to one concrete option", mailer1.msg.Subject)
	}
}

func TestHandlerRejectsMalformedPayload(t *testing.T) {
	task := asynq.NewTask(queue.TaskTestSend, []byte("not json"))
	if err := Handler(&stubCore{}, &stubMailer{})(context.Background(), task); err == nil {
		t.Fatal("err = nil, want an error for a malformed payload")
	}
}

// --- Defense-in-depth suppression re-check ----------------------------------
//
// docs/security.md: a test-send must never reach an address the workspace has
// unsubscribed or bounced. The API-side check in campaign.Service.TestSend is
// primary; these tests cover the worker's OWN re-check, which exists because
// that API check can race an incoming unsubscribe between enqueue and this
// task running.

// TestHandlerSkipsASuppressedRecipientWithoutSending is the headline: a
// suppressed `to` must short-circuit BEFORE any content is loaded, any
// credential decrypted, or the mailer invoked -- and it must not be reported
// as a task failure (no retry storm on an intentional skip).
func TestHandlerSkipsASuppressedRecipientWithoutSending(t *testing.T) {
	core := &stubCore{
		suppressed: true,
		content:    coreapi.TestSendContent{Subject: "Hi", FirstName: "Alex", Company: "Acme"},
		transport:  coreapi.SenderTransport{Provider: "smtp"},
	}
	mailer := &stubMailer{}

	if err := Handler(core, mailer)(context.Background(), testSendTask(t, defaultPayload())); err != nil {
		t.Fatalf("Handler: %v, want nil (a suppressed recipient is skipped, not failed)", err)
	}
	if mailer.calls != 0 {
		t.Error("a suppressed recipient must not be sent to")
	}
	if len(core.contentCalls) != 0 {
		t.Error("a suppressed recipient must not load test-send content")
	}
	if len(core.transportCalls) != 0 {
		t.Error("a suppressed recipient must not resolve (decrypt) a sender transport")
	}
	if len(core.suppressedCalls) != 1 || core.suppressedCalls[0] != [2]string{"ws-1", "preview@example.com"} {
		t.Errorf("suppression check calls = %+v, want [{ws-1 preview@example.com}]", core.suppressedCalls)
	}
}

// An unsuppressed recipient is unaffected: the full send path still runs.
func TestHandlerStillSendsToAnUnsuppressedRecipient(t *testing.T) {
	core := &stubCore{
		suppressed: false,
		content:    coreapi.TestSendContent{Subject: "Hi", FirstName: "Alex", Company: "Acme"},
		transport:  coreapi.SenderTransport{Provider: "smtp"},
	}
	mailer := &stubMailer{}

	if err := Handler(core, mailer)(context.Background(), testSendTask(t, defaultPayload())); err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if mailer.calls != 1 {
		t.Errorf("mailer calls = %d, want 1", mailer.calls)
	}
}

// A suppression-check failure (e.g. the DB is down) must fail the task rather
// than silently treat it as "not suppressed" and send anyway.
func TestHandlerPropagatesTheSuppressionCheckError(t *testing.T) {
	boom := errors.New("db unavailable")
	core := &stubCore{suppressedErr: boom}
	mailer := &stubMailer{}

	err := Handler(core, mailer)(context.Background(), testSendTask(t, defaultPayload()))
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the propagated suppression-check error", err)
	}
	if mailer.calls != 0 {
		t.Error("a suppression-check failure must not send anything")
	}
}
