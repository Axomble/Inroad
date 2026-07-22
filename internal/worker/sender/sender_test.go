package sender

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/queue"
)

const testBaseURL = "https://app.test"

var testTrackingSecret = []byte("0123456789abcdef0123456789abcdef")

// stubCore embeds coreapi.Client so it satisfies the interface; only the two
// methods Handler calls are implemented.
type stubCore struct {
	coreapi.Client
	job    coreapi.SendJob
	marked *coreapi.SendResult
}

func (s *stubCore) GetSendJob(context.Context, string, string) (coreapi.SendJob, error) {
	return s.job, nil
}
func (s *stubCore) MarkSend(_ context.Context, _, _ string, res coreapi.SendResult) error {
	s.marked = &res
	return nil
}

// stubSender records the single message passed to Send. Named distinctly
// from send_integration_test.go's fakeSender (same package, integration
// build tag) to avoid a redeclaration when both compile together.
type stubSender struct {
	sent mail.Message
	tj   mail.OutboundJob
}

func (f *stubSender) Send(_ context.Context, tj mail.OutboundJob, m mail.Message) (string, error) {
	f.tj, f.sent = tj, m
	return "<mid@x>", nil
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
	core := &stubCore{job: coreapi.SendJob{
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

// TestHandlerSkipsTrackingWhenDisabled proves TrackingEnabled=false leaves the
// HTML body exactly as personalize+withUnsubHTML produced it — no pixel, no
// rewritten links.
func TestHandlerSkipsTrackingWhenDisabled(t *testing.T) {
	html := `<html><body><a href="https://example.com/x">click</a></body></html>`
	core := &stubCore{job: coreapi.SendJob{
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
	core := &stubCore{job: coreapi.SendJob{
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
