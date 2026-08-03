package sender

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/queue"
)

const testBaseURL = "https://app.test"

var testTrackingSecret = []byte("0123456789abcdef0123456789abcdef")

// stubCore embeds coreapi.Client so it satisfies the interface; only the
// methods Handler calls are implemented.
type stubCore struct {
	coreapi.Client
	job    coreapi.SendJob
	marked *coreapi.SendResult
	// claimOK is the result of the FIRST ClaimSend; subsequent claims lose,
	// modelling the DB claim (a retried/raced job finds the row already
	// 'sending'/terminal and skips).
	claimOK    bool
	claimErr   error
	claimCalls int
	released   bool
}

func (s *stubCore) GetSendJob(context.Context, string, string) (coreapi.SendJob, error) {
	return s.job, nil
}
func (s *stubCore) ClaimSend(context.Context, string, string) (bool, error) {
	s.claimCalls++
	if s.claimErr != nil {
		return false, s.claimErr
	}
	return s.claimOK && s.claimCalls == 1, nil
}
func (s *stubCore) ReleaseSend(context.Context, string, string) error {
	s.released = true
	return nil
}
func (s *stubCore) MarkSend(_ context.Context, _, _ string, res coreapi.SendResult) error {
	s.marked = &res
	return nil
}

// stubSender records the message passed to Send and counts calls. Named
// distinctly from send_integration_test.go's fakeSender (same package,
// integration build tag) to avoid a redeclaration when both compile together.
type stubSender struct {
	calls int
	sent  mail.Message
	tj    mail.OutboundJob
	err   error
}

func (f *stubSender) Send(_ context.Context, tj mail.OutboundJob, m mail.Message) (string, error) {
	f.calls++
	f.tj, f.sent = tj, m
	return "<mid@x>", f.err
}

func sendTask(t *testing.T) *asynq.Task {
	t.Helper()
	b, err := json.Marshal(queue.SendEmailPayload{SendID: "11111111-1111-4111-8111-111111111111", WorkspaceID: "w1"})
	if err != nil {
		t.Fatal(err)
	}
	return asynq.NewTask(queue.TaskSendEmail, b)
}

// TestHandlerInjectsTrackingWhenEnabled proves that when the job's campaign
// has tracking enabled, the sent HTML body carries a rewritten click link and
// an open pixel, the unsubscribe link is left untouched, and the plain-text
// body is never rewritten.
func TestHandlerInjectsTrackingWhenEnabled(t *testing.T) {
	core := &stubCore{claimOK: true, job: coreapi.SendJob{
		SendID: "11111111-1111-4111-8111-111111111111", EffectiveDailyCap: 10, ToEmail: "a@b.io",
		Provider: "smtp", Subject: "Hi", BodyText: "hello",
		BodyHTML:        `<html><body><p>hello <a href="https://example.com/x">click</a></p></body></html>`,
		UnsubURL:        testBaseURL + "/u/tok",
		TrackingEnabled: true,
	}}
	snd := &stubSender{}
	h := Handler(core, snd, nil, testBaseURL, testTrackingSecret)
	if err := h(context.Background(), sendTask(t)); err != nil {
		t.Fatal(err)
	}
	// The job's provider must propagate into the dispatched OutboundJob so
	// MultiSender routes to the right transport (default SMTP path here).
	if snd.tj.Provider != "smtp" {
		t.Errorf("expected OutboundJob.Provider=smtp, got %q", snd.tj.Provider)
	}
	if !strings.Contains(snd.sent.BodyHTML, "/t/c/") {
		t.Errorf("expected a rewritten click link, got %q", snd.sent.BodyHTML)
	}
	if !strings.Contains(snd.sent.BodyHTML, "/t/o/") {
		t.Errorf("expected an open pixel, got %q", snd.sent.BodyHTML)
	}
	if !strings.Contains(snd.sent.BodyHTML, testBaseURL+"/u/tok") {
		t.Errorf("unsubscribe link must remain untouched, got %q", snd.sent.BodyHTML)
	}
	if strings.Contains(snd.sent.BodyText, "/t/") {
		t.Errorf("plain-text body must never be rewritten, got %q", snd.sent.BodyText)
	}
}

// TestHandlerGmailProviderDispatchesAccessToken proves the outbound Gmail path:
// a job with Provider="gmail" and a decrypted access token propagates both into
// the dispatched OutboundJob, so MultiSender routes to the Gmail transport with
// the right bearer (rather than the SMTP leg).
func TestHandlerGmailProviderDispatchesAccessToken(t *testing.T) {
	core := &stubCore{claimOK: true, job: coreapi.SendJob{
		SendID: "11111111-1111-4111-8111-111111111111", EffectiveDailyCap: 10, ToEmail: "a@b.io",
		Provider: "gmail", AccessToken: []byte("ya29.access-token"),
		Subject: "Hi", BodyText: "hello",
	}}
	snd := &stubSender{}
	h := Handler(core, snd, nil, testBaseURL, testTrackingSecret)
	if err := h(context.Background(), sendTask(t)); err != nil {
		t.Fatal(err)
	}
	if snd.tj.Provider != "gmail" {
		t.Errorf("expected OutboundJob.Provider=gmail, got %q", snd.tj.Provider)
	}
	if snd.tj.AccessToken != "ya29.access-token" {
		t.Errorf("expected the job's access token forwarded to the OutboundJob, got %q", snd.tj.AccessToken)
	}
}

// TestHandlerSkipsTrackingWhenDisabled proves TrackingEnabled=false leaves the
// HTML body exactly as personalize+withUnsubHTML produced it — no pixel, no
// rewritten links.
func TestHandlerSkipsTrackingWhenDisabled(t *testing.T) {
	html := `<html><body><a href="https://example.com/x">click</a></body></html>`
	core := &stubCore{claimOK: true, job: coreapi.SendJob{
		SendID: "11111111-1111-4111-8111-111111111111", EffectiveDailyCap: 10, ToEmail: "a@b.io",
		Subject: "Hi", BodyText: "hello", BodyHTML: html, TrackingEnabled: false,
	}}
	snd := &stubSender{}
	h := Handler(core, snd, nil, testBaseURL, testTrackingSecret)
	if err := h(context.Background(), sendTask(t)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(snd.sent.BodyHTML, "/t/c/") || strings.Contains(snd.sent.BodyHTML, "/t/o/") {
		t.Errorf("tracking disabled must leave the HTML unrewritten, got %q", snd.sent.BodyHTML)
	}
	if !strings.Contains(snd.sent.BodyHTML, `href="https://example.com/x"`) {
		t.Errorf("original link must be preserved verbatim, got %q", snd.sent.BodyHTML)
	}
}

// TestHandlerNoHTMLBodyNoInjection proves a text-only send is untouched by the
// tracking rewrite even when TrackingEnabled is true — RewriteHTML is never
// called on an empty body.
func TestHandlerNoHTMLBodyNoInjection(t *testing.T) {
	core := &stubCore{claimOK: true, job: coreapi.SendJob{
		SendID: "11111111-1111-4111-8111-111111111111", EffectiveDailyCap: 10, ToEmail: "a@b.io",
		Subject: "Hi", BodyText: "text only", TrackingEnabled: true,
	}}
	snd := &stubSender{}
	h := Handler(core, snd, nil, testBaseURL, testTrackingSecret)
	if err := h(context.Background(), sendTask(t)); err != nil {
		t.Fatal(err)
	}
	if snd.sent.BodyHTML != "" {
		t.Errorf("no HTML body in the job must yield no HTML in the sent message, got %q", snd.sent.BodyHTML)
	}
}

// TestHandlerDoubleSendDeliversOnce is the direct-path headline regression:
// invoking the send handler twice for the SAME send delivers exactly once. The
// second ClaimSend loses (the row is already owned/terminal), so Send is never
// called a second time.
func TestHandlerDoubleSendDeliversOnce(t *testing.T) {
	core := &stubCore{claimOK: true, job: coreapi.SendJob{
		SendID: "11111111-1111-4111-8111-111111111111", EffectiveDailyCap: 10, ToEmail: "a@b.io",
		Provider: "smtp", Subject: "Hi", BodyText: "hello",
	}}
	snd := &stubSender{}
	h := Handler(core, snd, nil, testBaseURL, testTrackingSecret)

	if err := h(context.Background(), sendTask(t)); err != nil {
		t.Fatalf("first send: %v", err)
	}
	if err := h(context.Background(), sendTask(t)); err != nil {
		t.Fatalf("second send: %v", err)
	}

	if core.claimCalls != 2 {
		t.Fatalf("expected two claim attempts, got %d", core.claimCalls)
	}
	if snd.calls != 1 {
		t.Fatalf("double-send bug: Send must be called EXACTLY once, got %d", snd.calls)
	}
}

// TestHandlerTransientSendReleasesAndReturnsError proves a transient failure
// releases the claim and returns the error (asynq retries) rather than
// finalizing the send as failed.
func TestHandlerTransientSendReleasesAndReturnsError(t *testing.T) {
	core := &stubCore{claimOK: true, job: coreapi.SendJob{
		SendID: "11111111-1111-4111-8111-111111111111", EffectiveDailyCap: 10, ToEmail: "a@b.io",
		Provider: "smtp", Subject: "Hi", BodyText: "hello",
	}}
	snd := &stubSender{err: &net.OpError{Op: "dial", Err: timeoutStub{}}}
	h := Handler(core, snd, nil, testBaseURL, testTrackingSecret)
	err := h(context.Background(), sendTask(t))
	if err == nil {
		t.Fatal("transient failure must return an error so asynq retries")
	}
	if !core.released {
		t.Fatal("transient failure must release the claim")
	}
	if core.marked != nil {
		t.Fatalf("transient failure must NOT finalize the send, got %+v", core.marked)
	}
}

// timeoutStub is a net.Error reporting a timeout (forces mail.Retryable=true).
type timeoutStub struct{}

func (timeoutStub) Error() string   { return "i/o timeout" }
func (timeoutStub) Timeout() bool   { return true }
func (timeoutStub) Temporary() bool { return true }

// deferEnq records the deferral the handler asked for. Handler takes the
// DelayedSendEnqueuer seam precisely so this is possible: it used to take the
// concrete *queue.Client, and every test passed nil, which left both deferral
// branches unreachable.
type deferEnq struct {
	calls int
	delay time.Duration
}

func (d *deferEnq) EnqueueSendIn(_, _ string, delay time.Duration) error {
	d.calls++
	d.delay = delay
	return nil
}

// Only a 'running' campaign may send, on the direct path as well as the step path.
//
// This path is DORMANT — EnqueueSends, the only writer of 'queued' campaign sends,
// has no production callers — so the gate is insurance rather than a live guard.
// It is here so "a campaign that is not running does not send" is a property of the
// codebase and not of one call site: an invariant that holds on only the live path
// rots the moment someone revives this one.
//
// The campaign status itself is resolved in GetSendJob (which also returns before
// unsealing any credential); the handler's job is to DEFER rather than finalize, so
// the row survives to be sent when an operator relaunches.
func TestPausedCampaignDefersOnTheDirectPath(t *testing.T) {
	core := &stubCore{claimOK: true, job: coreapi.SendJob{
		CampaignPaused: true,
		// Deliberately also over cap and suppressible-looking: the pause branch must
		// win before the cap branch bumps attempts.
		EffectiveDailyCap: 0, SentToday: 5,
		ToEmail: "a@b.test", Subject: "Hi", BodyText: "yo",
	}}
	snd, enq := &stubSender{}, &deferEnq{}

	if err := Handler(core, snd, enq, testBaseURL, testTrackingSecret)(context.Background(), sendTask(t)); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if snd.calls != 0 {
		t.Errorf("a paused campaign sent %d messages", snd.calls)
	}
	if core.claimCalls != 0 {
		t.Errorf("a paused campaign claimed the send row (%d claims)", core.claimCalls)
	}
	// Deferred, not finalized: the row must still be sendable after a relaunch.
	if core.marked != nil {
		t.Errorf("a paused campaign finalized the send as %q", core.marked.Status)
	}
	if enq.calls != 1 || enq.delay != campaignPausedBackoff {
		t.Errorf("deferrals = %d at %v, want 1 at %v", enq.calls, enq.delay, campaignPausedBackoff)
	}
}

// The converse: a running campaign still sends, so the gate is not stuck closed.
func TestRunningCampaignStillSendsOnTheDirectPath(t *testing.T) {
	core := &stubCore{claimOK: true, job: coreapi.SendJob{
		EffectiveDailyCap: 100, SentToday: 0,
		ToEmail: "a@b.test", Subject: "Hi", BodyText: "yo",
	}}
	snd, enq := &stubSender{}, &deferEnq{}

	if err := Handler(core, snd, enq, testBaseURL, testTrackingSecret)(context.Background(), sendTask(t)); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if snd.calls != 1 {
		t.Fatalf("a running campaign sent %d messages, want 1", snd.calls)
	}
	if enq.calls != 0 {
		t.Errorf("a running campaign was deferred %d times", enq.calls)
	}
	if core.marked == nil || core.marked.Status != "sent" {
		t.Errorf("send not finalized 'sent': %+v", core.marked)
	}
}
