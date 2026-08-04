package campaign_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/app/campaign"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/mail"
)

// --- fakes: the mail sender seam, the credential resolver, the limiter -----

type mailCall struct {
	job mail.OutboundJob
	msg mail.Message
}

// fakeMailer is TestSend's mocked mail sender seam: it never dials out, it
// just records what it was asked to send.
type fakeMailer struct {
	calls []mailCall
	err   error
}

func (f *fakeMailer) Send(_ context.Context, tj mail.OutboundJob, msg mail.Message) (string, error) {
	f.calls = append(f.calls, mailCall{tj, msg})
	if f.err != nil {
		return "", f.err
	}
	return "test-message-id", nil
}

type resolveCall struct{ ws, mailboxID uuid.UUID }

// fakeSenderResolver is the mocked credential resolver: it never touches a
// Keyring or an OAuth token, it just hands back a fixed transport and records
// which mailbox it was asked to resolve.
type fakeSenderResolver struct {
	transport campaign.SenderTransport
	err       error
	calls     []resolveCall
}

func (f *fakeSenderResolver) ResolveSenderTransport(_ context.Context, ws, mailboxID uuid.UUID) (campaign.SenderTransport, error) {
	f.calls = append(f.calls, resolveCall{ws, mailboxID})
	return f.transport, f.err
}

// fakeRateLimiter is the mocked RateLimiter: allow/err are set per test,
// calls records every key it was asked about.
type fakeRateLimiter struct {
	allow bool
	err   error
	calls []string
}

func (f *fakeRateLimiter) Allow(_ context.Context, key string, _ int, _ time.Duration) (bool, error) {
	f.calls = append(f.calls, key)
	return f.allow, f.err
}

// --- Service.TestSend --------------------------------------------------------

// testSendFixture bundles a ready-to-use campaign/step/store so most tests
// only need to override what they're actually exercising.
func testSendFixture(ws, id, stepID uuid.UUID) *fakeCampaignStore {
	return &fakeCampaignStore{
		campaigns: map[[2]uuid.UUID]gen.Campaign{{ws, id}: {ID: id, WorkspaceID: ws}},
		steps: []gen.SequenceStep{
			{ID: stepID, Subject: "Hi {{first_name}} from {{company}}", BodyText: "text {{first_name}}", BodyHtml: "<p>{{company}}</p>"},
		},
		senders: []campaign.Sender{{MailboxID: uuid.New(), Email: "ok@x.test", Enabled: true, Status: "active"}},
	}
}

func TestTestSendPrefixesSubjectAndSkipsTrackingAndUnsub(t *testing.T) {
	ctx := context.Background()
	ws, id, stepID := uuid.New(), uuid.New(), uuid.New()
	store := testSendFixture(ws, id, stepID)
	store.firstName, store.firstCompany, store.firstFound = "Ada", "Analytical Engines", true

	mailer := &fakeMailer{}
	resolver := &fakeSenderResolver{transport: campaign.SenderTransport{
		FromEmail: "ok@x.test", FromName: "Ok Sender",
		OutboundJob: mail.OutboundJob{Provider: "smtp"},
	}}
	svc := campaign.NewService(store, noopChecker{},
		campaign.WithSenderResolver(resolver), campaign.WithMailer(mailer), campaign.WithRateLimiter(&fakeRateLimiter{allow: true}))

	if err := svc.TestSend(ctx, ws, id, stepID, "preview@example.com"); err != nil {
		t.Fatalf("TestSend: %v", err)
	}
	if len(mailer.calls) != 1 {
		t.Fatalf("mailer calls = %d, want 1", len(mailer.calls))
	}
	got := mailer.calls[0].msg
	if got.Subject != "[Test] Hi Ada from Analytical Engines" {
		t.Errorf("subject = %q, want the [Test] prefix over the rendered subject", got.Subject)
	}
	if got.BodyText != "text Ada" {
		t.Errorf("body_text = %q", got.BodyText)
	}
	if got.BodyHTML != "<p>Analytical Engines</p>" {
		t.Errorf("body_html = %q, want the rendered template with no tracking rewrite", got.BodyHTML)
	}
	if got.ListUnsubscribe != "" {
		t.Errorf("ListUnsubscribe = %q, want empty -- a test-send is never subject to suppression/unsubscribe", got.ListUnsubscribe)
	}
	if got.To != "preview@example.com" {
		t.Errorf("To = %q, want the requested preview recipient (not the sample contact's own email)", got.To)
	}
	if got.FromEmail != "ok@x.test" || got.FromName != "Ok Sender" {
		t.Errorf("from = %q/%q, want the resolved sender's identity", got.FromEmail, got.FromName)
	}
}

func TestTestSendSelectsTheFirstEnabledAndActiveMailbox(t *testing.T) {
	ctx := context.Background()
	ws, id, stepID := uuid.New(), uuid.New(), uuid.New()
	store := testSendFixture(ws, id, stepID)
	disabled, paused, eligible := uuid.New(), uuid.New(), uuid.New()
	store.senders = []campaign.Sender{
		{MailboxID: disabled, Email: "disabled@x.test", Enabled: false, Status: "active"},
		{MailboxID: paused, Email: "paused@x.test", Enabled: true, Status: "paused"},
		{MailboxID: eligible, Email: "eligible@x.test", Enabled: true, Status: "active"},
	}
	resolver := &fakeSenderResolver{}
	svc := campaign.NewService(store, noopChecker{},
		campaign.WithSenderResolver(resolver), campaign.WithMailer(&fakeMailer{}), campaign.WithRateLimiter(&fakeRateLimiter{allow: true}))

	if err := svc.TestSend(ctx, ws, id, stepID, "preview@example.com"); err != nil {
		t.Fatalf("TestSend: %v", err)
	}
	if len(resolver.calls) != 1 || resolver.calls[0].mailboxID != eligible {
		t.Errorf("resolver calls = %+v, want exactly the eligible mailbox %s", resolver.calls, eligible)
	}
}

func TestTestSendFallsBackToSyntheticVarsWhenTheListIsEmpty(t *testing.T) {
	ctx := context.Background()
	ws, id, stepID := uuid.New(), uuid.New(), uuid.New()
	store := testSendFixture(ws, id, stepID) // firstFound left false: no contact
	mailer := &fakeMailer{}
	svc := campaign.NewService(store, noopChecker{},
		campaign.WithSenderResolver(&fakeSenderResolver{}), campaign.WithMailer(mailer), campaign.WithRateLimiter(&fakeRateLimiter{allow: true}))

	if err := svc.TestSend(ctx, ws, id, stepID, "preview@example.com"); err != nil {
		t.Fatalf("TestSend: %v", err)
	}
	if got := mailer.calls[0].msg.Subject; got != "[Test] Hi Alex from Acme" {
		t.Errorf("subject = %q, want the synthetic fallback vars (first_name=Alex, company=Acme)", got)
	}
}

func TestTestSendReturns422WhenNoEligibleSender(t *testing.T) {
	ctx := context.Background()
	ws, id, stepID := uuid.New(), uuid.New(), uuid.New()
	store := testSendFixture(ws, id, stepID)
	store.senders = []campaign.Sender{{MailboxID: uuid.New(), Email: "x@x.test", Enabled: false, Status: "active"}}
	mailer := &fakeMailer{}
	svc := campaign.NewService(store, noopChecker{},
		campaign.WithSenderResolver(&fakeSenderResolver{}), campaign.WithMailer(mailer), campaign.WithRateLimiter(&fakeRateLimiter{allow: true}))

	err := svc.TestSend(ctx, ws, id, stepID, "preview@example.com")
	if !errors.Is(err, campaign.ErrNoEligibleSender) {
		t.Fatalf("err = %v, want ErrNoEligibleSender", err)
	}
	if len(mailer.calls) != 0 {
		t.Error("no eligible sender must not send anything")
	}
}

func TestTestSendCrossTenantIsNotFound(t *testing.T) {
	ctx := context.Background()
	owner, id, stepID := uuid.New(), uuid.New(), uuid.New()
	store := testSendFixture(owner, id, stepID)
	mailer := &fakeMailer{}
	svc := campaign.NewService(store, noopChecker{},
		campaign.WithSenderResolver(&fakeSenderResolver{}), campaign.WithMailer(mailer), campaign.WithRateLimiter(&fakeRateLimiter{allow: true}))

	err := svc.TestSend(ctx, uuid.New(), id, stepID, "preview@example.com")
	if !errors.Is(err, campaign.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if len(mailer.calls) != 0 {
		t.Error("a cross-tenant campaign id must not send anything")
	}
}

func TestTestSendUnknownStepIsNotFound(t *testing.T) {
	ctx := context.Background()
	ws, id, stepID := uuid.New(), uuid.New(), uuid.New()
	store := testSendFixture(ws, id, stepID)
	mailer := &fakeMailer{}
	svc := campaign.NewService(store, noopChecker{},
		campaign.WithSenderResolver(&fakeSenderResolver{}), campaign.WithMailer(mailer), campaign.WithRateLimiter(&fakeRateLimiter{allow: true}))

	err := svc.TestSend(ctx, ws, id, uuid.New() /* not the fixture's step */, "preview@example.com")
	if !errors.Is(err, campaign.ErrStepNotFound) {
		t.Fatalf("err = %v, want ErrStepNotFound", err)
	}
	if len(mailer.calls) != 0 {
		t.Error("an unknown step id must not send anything")
	}
}

func TestTestSendRateLimited(t *testing.T) {
	ctx := context.Background()
	ws, id, stepID := uuid.New(), uuid.New(), uuid.New()
	store := testSendFixture(ws, id, stepID)
	mailer := &fakeMailer{}
	limiter := &fakeRateLimiter{allow: false}
	svc := campaign.NewService(store, noopChecker{},
		campaign.WithSenderResolver(&fakeSenderResolver{}), campaign.WithMailer(mailer), campaign.WithRateLimiter(limiter))

	err := svc.TestSend(ctx, ws, id, stepID, "preview@example.com")
	if !errors.Is(err, campaign.ErrTestSendRateLimited) {
		t.Fatalf("err = %v, want ErrTestSendRateLimited", err)
	}
	if len(mailer.calls) != 0 {
		t.Error("a rate-limited request must not send anything")
	}
	if len(limiter.calls) != 1 || !strings.Contains(limiter.calls[0], ws.String()) {
		t.Errorf("limiter key = %v, want it scoped to the workspace", limiter.calls)
	}
}

// A limiter backing-store failure must fail CLOSED (deny), not silently lift
// the cap -- the same posture internal/platform/throttle documents.
func TestTestSendRateLimiterErrorFailsClosed(t *testing.T) {
	ctx := context.Background()
	ws, id, stepID := uuid.New(), uuid.New(), uuid.New()
	store := testSendFixture(ws, id, stepID)
	mailer := &fakeMailer{}
	limiter := &fakeRateLimiter{allow: true, err: errors.New("redis unreachable")}
	svc := campaign.NewService(store, noopChecker{},
		campaign.WithSenderResolver(&fakeSenderResolver{}), campaign.WithMailer(mailer), campaign.WithRateLimiter(limiter))

	err := svc.TestSend(ctx, ws, id, stepID, "preview@example.com")
	if !errors.Is(err, campaign.ErrTestSendRateLimited) {
		t.Fatalf("err = %v, want ErrTestSendRateLimited (fail closed on a limiter error)", err)
	}
	if len(mailer.calls) != 0 {
		t.Error("a limiter error must not send anything")
	}
}

// No RateLimiter wired at all is a deployment choice (unlimited), not a
// missing-dependency failure.
func TestTestSendWithoutARateLimiterIsUnlimited(t *testing.T) {
	ctx := context.Background()
	ws, id, stepID := uuid.New(), uuid.New(), uuid.New()
	store := testSendFixture(ws, id, stepID)
	mailer := &fakeMailer{}
	svc := campaign.NewService(store, noopChecker{},
		campaign.WithSenderResolver(&fakeSenderResolver{}), campaign.WithMailer(mailer))

	if err := svc.TestSend(ctx, ws, id, stepID, "preview@example.com"); err != nil {
		t.Fatalf("TestSend: %v", err)
	}
	if len(mailer.calls) != 1 {
		t.Errorf("mailer calls = %d, want 1", len(mailer.calls))
	}
}

func TestTestSendWithoutSenderOrMailerConfiguredErrorsRatherThanPanics(t *testing.T) {
	ctx := context.Background()
	ws, id, stepID := uuid.New(), uuid.New(), uuid.New()
	store := testSendFixture(ws, id, stepID)
	svc := campaign.NewService(store, noopChecker{}) // no test-send deps wired at all

	if err := svc.TestSend(ctx, ws, id, stepID, "preview@example.com"); err == nil {
		t.Fatal("err = nil, want an error when test-send has no resolver/mailer wired")
	}
}

func TestTestSendPropagatesTheResolverError(t *testing.T) {
	ctx := context.Background()
	ws, id, stepID := uuid.New(), uuid.New(), uuid.New()
	store := testSendFixture(ws, id, stepID)
	boom := errors.New("keyring unavailable")
	svc := campaign.NewService(store, noopChecker{},
		campaign.WithSenderResolver(&fakeSenderResolver{err: boom}), campaign.WithMailer(&fakeMailer{}), campaign.WithRateLimiter(&fakeRateLimiter{allow: true}))

	if err := svc.TestSend(ctx, ws, id, stepID, "preview@example.com"); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the propagated resolver error", err)
	}
}

// --- Handler.testSend: HTTP status mapping ----------------------------------

// noopEnqueuer satisfies campaign.Enqueuer; testSend never calls Launch.
type noopEnqueuer struct{}

func (noopEnqueuer) EnqueueAdvanceAt(string, string, time.Time) error { return nil }

// serveTestSend runs one request through the REAL auth middleware and the
// campaign router's own chi mux (mounted under /campaigns exactly as cmd/inroad
// mounts it), so {id} is parsed by chi exactly as in production and
// RequireVerified (mirrored from launch's middleware line) actually runs.
func serveTestSend(t *testing.T, h *campaign.Handler, secret []byte, ws, campaignID uuid.UUID, body string) *httptest.ResponseRecorder {
	t.Helper()
	tok, err := auth.IssueToken(secret, auth.Claims{
		UserID: uuid.New().String(), WorkspaceID: ws.String(), Role: "owner", SessionID: uuid.New().String(),
	}, time.Hour)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/campaigns/"+campaignID.String()+"/test-send", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)

	root := chi.NewRouter()
	root.Mount("/campaigns", h.Routes(alwaysVerifiedChecker{}))

	w := httptest.NewRecorder()
	auth.RequireAuth(auth.NewJWTVerifier(secret))(root).ServeHTTP(w, req)
	return w
}

func TestTestSendHandlerReturns202AndSentTrue(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	ws, id, stepID := uuid.New(), uuid.New(), uuid.New()
	store := testSendFixture(ws, id, stepID)
	h := campaign.NewHandler(campaign.NewService(store, noopChecker{},
		campaign.WithSenderResolver(&fakeSenderResolver{}), campaign.WithMailer(&fakeMailer{}), campaign.WithRateLimiter(&fakeRateLimiter{allow: true})),
		noopEnqueuer{})

	body := `{"step_id":"` + stepID.String() + `","to":"preview@example.com"}`
	w := serveTestSend(t, h, secret, ws, id, body)

	if w.Code != http.StatusAccepted {
		t.Fatalf("code = %d, want 202: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"sent":true`) {
		t.Errorf("body = %s, want {\"sent\":true}", w.Body.String())
	}
}

func TestTestSendHandlerRejectsInvalidBody(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	ws, id, stepID := uuid.New(), uuid.New(), uuid.New()

	cases := []struct {
		name string
		body string
	}{
		{"malformed json", `{"step_id":`},
		{"missing step_id", `{"to":"preview@example.com"}`},
		{"bad step_id", `{"step_id":"not-a-uuid","to":"preview@example.com"}`},
		{"missing to", `{"step_id":"` + stepID.String() + `"}`},
		{"bad email", `{"step_id":"` + stepID.String() + `","to":"not-an-email"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := testSendFixture(ws, id, stepID)
			h := campaign.NewHandler(campaign.NewService(store, noopChecker{},
				campaign.WithSenderResolver(&fakeSenderResolver{}), campaign.WithMailer(&fakeMailer{}), campaign.WithRateLimiter(&fakeRateLimiter{allow: true})),
				noopEnqueuer{})

			w := serveTestSend(t, h, secret, ws, id, tc.body)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("code = %d, want 400: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestTestSendHandlerReturns422ForNoEligibleSender(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	ws, id, stepID := uuid.New(), uuid.New(), uuid.New()
	store := testSendFixture(ws, id, stepID)
	store.senders = []campaign.Sender{{MailboxID: uuid.New(), Email: "x@x.test", Enabled: false, Status: "active"}}
	h := campaign.NewHandler(campaign.NewService(store, noopChecker{},
		campaign.WithSenderResolver(&fakeSenderResolver{}), campaign.WithMailer(&fakeMailer{}), campaign.WithRateLimiter(&fakeRateLimiter{allow: true})),
		noopEnqueuer{})

	body := `{"step_id":"` + stepID.String() + `","to":"preview@example.com"}`
	w := serveTestSend(t, h, secret, ws, id, body)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("code = %d, want 422: %s", w.Code, w.Body.String())
	}
}

func TestTestSendHandlerCrossTenantIsNotFound(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	owner, id, stepID := uuid.New(), uuid.New(), uuid.New()
	store := testSendFixture(owner, id, stepID)
	h := campaign.NewHandler(campaign.NewService(store, noopChecker{},
		campaign.WithSenderResolver(&fakeSenderResolver{}), campaign.WithMailer(&fakeMailer{}), campaign.WithRateLimiter(&fakeRateLimiter{allow: true})),
		noopEnqueuer{})

	intruder := uuid.New()
	body := `{"step_id":"` + stepID.String() + `","to":"preview@example.com"}`
	w := serveTestSend(t, h, secret, intruder, id, body)

	if w.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404: %s", w.Code, w.Body.String())
	}
}

// alwaysVerifiedChecker satisfies auth.VerifiedChecker so Routes()'s
// RequireVerified middleware (mirrored from launch's middleware line) passes
// in tests.
type alwaysVerifiedChecker struct{}

func (alwaysVerifiedChecker) IsEmailVerified(context.Context, uuid.UUID) (bool, error) {
	return true, nil
}
