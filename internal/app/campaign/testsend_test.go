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
)

// --- fakes: the enqueue seam, the limiter -----------------------------------
//
// ARCHITECTURE (docs/security.md invariant 1): cmd/inroad never decrypts a
// mailbox credential or dials a provider. There is therefore no Mailer/
// SenderResolver seam left in this package to mock -- TestSend's only
// side effect is enqueuing a testsend:send task, which
// internal/worker/testsend (a different process) renders and sends. Every
// test below that asserts "err == nil" is implicitly also proving no send
// happened API-side, because nothing capable of sending exists in this
// package at all.

type testSendCall struct {
	campaignID, stepID, mailboxID, to, workspaceID string
}

// fakeTestSendEnqueuer is TestSend's mocked enqueue seam: it never touches
// Redis, it just records the payload it was asked to enqueue.
type fakeTestSendEnqueuer struct {
	calls []testSendCall
	err   error
}

func (f *fakeTestSendEnqueuer) EnqueueTestSend(campaignID, stepID, mailboxID, to, workspaceID string) error {
	f.calls = append(f.calls, testSendCall{campaignID, stepID, mailboxID, to, workspaceID})
	return f.err
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
// only need to override what they're actually exercising. The step content
// is irrelevant now (rendering moved to internal/worker/testsend) -- only its
// id and campaign ownership matter here.
func testSendFixture(ws, id, stepID uuid.UUID) *fakeCampaignStore {
	return &fakeCampaignStore{
		campaigns: map[[2]uuid.UUID]gen.Campaign{{ws, id}: {ID: id, WorkspaceID: ws}},
		steps:     []gen.SequenceStep{{ID: stepID}},
		senders:   []campaign.Sender{{MailboxID: uuid.New(), Email: "ok@x.test", Enabled: true, Status: "active"}},
	}
}

func TestTestSendEnqueuesWithTheResolvedMailboxAndRequestFields(t *testing.T) {
	ctx := context.Background()
	ws, id, stepID := uuid.New(), uuid.New(), uuid.New()
	mailboxID := uuid.New()
	store := testSendFixture(ws, id, stepID)
	store.senders = []campaign.Sender{{MailboxID: mailboxID, Email: "ok@x.test", Enabled: true, Status: "active"}}

	enq := &fakeTestSendEnqueuer{}
	svc := campaign.NewService(store, noopChecker{},
		campaign.WithTestSendEnqueuer(enq), campaign.WithRateLimiter(&fakeRateLimiter{allow: true}))

	if err := svc.TestSend(ctx, ws, id, stepID, "preview@example.com"); err != nil {
		t.Fatalf("TestSend: %v", err)
	}
	if len(enq.calls) != 1 {
		t.Fatalf("enqueue calls = %d, want 1", len(enq.calls))
	}
	got := enq.calls[0]
	want := testSendCall{
		campaignID: id.String(), stepID: stepID.String(), mailboxID: mailboxID.String(),
		to: "preview@example.com", workspaceID: ws.String(),
	}
	if got != want {
		t.Errorf("enqueued payload = %+v, want %+v", got, want)
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
	enq := &fakeTestSendEnqueuer{}
	svc := campaign.NewService(store, noopChecker{},
		campaign.WithTestSendEnqueuer(enq), campaign.WithRateLimiter(&fakeRateLimiter{allow: true}))

	if err := svc.TestSend(ctx, ws, id, stepID, "preview@example.com"); err != nil {
		t.Fatalf("TestSend: %v", err)
	}
	if len(enq.calls) != 1 || enq.calls[0].mailboxID != eligible.String() {
		t.Errorf("enqueue calls = %+v, want exactly the eligible mailbox %s", enq.calls, eligible)
	}
}

func TestTestSendReturns422WhenNoEligibleSender(t *testing.T) {
	ctx := context.Background()
	ws, id, stepID := uuid.New(), uuid.New(), uuid.New()
	store := testSendFixture(ws, id, stepID)
	store.senders = []campaign.Sender{{MailboxID: uuid.New(), Email: "x@x.test", Enabled: false, Status: "active"}}
	enq := &fakeTestSendEnqueuer{}
	svc := campaign.NewService(store, noopChecker{},
		campaign.WithTestSendEnqueuer(enq), campaign.WithRateLimiter(&fakeRateLimiter{allow: true}))

	err := svc.TestSend(ctx, ws, id, stepID, "preview@example.com")
	if !errors.Is(err, campaign.ErrNoEligibleSender) {
		t.Fatalf("err = %v, want ErrNoEligibleSender", err)
	}
	if len(enq.calls) != 0 {
		t.Error("no eligible sender must not enqueue anything")
	}
}

func TestTestSendCrossTenantIsNotFound(t *testing.T) {
	ctx := context.Background()
	owner, id, stepID := uuid.New(), uuid.New(), uuid.New()
	store := testSendFixture(owner, id, stepID)
	enq := &fakeTestSendEnqueuer{}
	svc := campaign.NewService(store, noopChecker{},
		campaign.WithTestSendEnqueuer(enq), campaign.WithRateLimiter(&fakeRateLimiter{allow: true}))

	err := svc.TestSend(ctx, uuid.New(), id, stepID, "preview@example.com")
	if !errors.Is(err, campaign.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if len(enq.calls) != 0 {
		t.Error("a cross-tenant campaign id must not enqueue anything")
	}
}

func TestTestSendUnknownStepIsNotFound(t *testing.T) {
	ctx := context.Background()
	ws, id, stepID := uuid.New(), uuid.New(), uuid.New()
	store := testSendFixture(ws, id, stepID)
	enq := &fakeTestSendEnqueuer{}
	svc := campaign.NewService(store, noopChecker{},
		campaign.WithTestSendEnqueuer(enq), campaign.WithRateLimiter(&fakeRateLimiter{allow: true}))

	err := svc.TestSend(ctx, ws, id, uuid.New() /* not the fixture's step */, "preview@example.com")
	if !errors.Is(err, campaign.ErrStepNotFound) {
		t.Fatalf("err = %v, want ErrStepNotFound", err)
	}
	if len(enq.calls) != 0 {
		t.Error("an unknown step id must not enqueue anything")
	}
}

func TestTestSendRateLimited(t *testing.T) {
	ctx := context.Background()
	ws, id, stepID := uuid.New(), uuid.New(), uuid.New()
	store := testSendFixture(ws, id, stepID)
	enq := &fakeTestSendEnqueuer{}
	limiter := &fakeRateLimiter{allow: false}
	svc := campaign.NewService(store, noopChecker{},
		campaign.WithTestSendEnqueuer(enq), campaign.WithRateLimiter(limiter))

	err := svc.TestSend(ctx, ws, id, stepID, "preview@example.com")
	if !errors.Is(err, campaign.ErrTestSendRateLimited) {
		t.Fatalf("err = %v, want ErrTestSendRateLimited", err)
	}
	if len(enq.calls) != 0 {
		t.Error("a rate-limited request must not enqueue anything")
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
	enq := &fakeTestSendEnqueuer{}
	limiter := &fakeRateLimiter{allow: true, err: errors.New("redis unreachable")}
	svc := campaign.NewService(store, noopChecker{},
		campaign.WithTestSendEnqueuer(enq), campaign.WithRateLimiter(limiter))

	err := svc.TestSend(ctx, ws, id, stepID, "preview@example.com")
	if !errors.Is(err, campaign.ErrTestSendRateLimited) {
		t.Fatalf("err = %v, want ErrTestSendRateLimited (fail closed on a limiter error)", err)
	}
	if len(enq.calls) != 0 {
		t.Error("a limiter error must not enqueue anything")
	}
}

// No RateLimiter wired at all is a deployment choice (unlimited), not a
// missing-dependency failure.
func TestTestSendWithoutARateLimiterIsUnlimited(t *testing.T) {
	ctx := context.Background()
	ws, id, stepID := uuid.New(), uuid.New(), uuid.New()
	store := testSendFixture(ws, id, stepID)
	enq := &fakeTestSendEnqueuer{}
	svc := campaign.NewService(store, noopChecker{}, campaign.WithTestSendEnqueuer(enq))

	if err := svc.TestSend(ctx, ws, id, stepID, "preview@example.com"); err != nil {
		t.Fatalf("TestSend: %v", err)
	}
	if len(enq.calls) != 1 {
		t.Errorf("enqueue calls = %d, want 1", len(enq.calls))
	}
}

func TestTestSendWithoutAnEnqueuerConfiguredErrorsRatherThanPanics(t *testing.T) {
	ctx := context.Background()
	ws, id, stepID := uuid.New(), uuid.New(), uuid.New()
	store := testSendFixture(ws, id, stepID)
	svc := campaign.NewService(store, noopChecker{}) // no test-send deps wired at all

	if err := svc.TestSend(ctx, ws, id, stepID, "preview@example.com"); err == nil {
		t.Fatal("err = nil, want an error when test-send has no enqueuer wired")
	}
}

func TestTestSendPropagatesTheEnqueueError(t *testing.T) {
	ctx := context.Background()
	ws, id, stepID := uuid.New(), uuid.New(), uuid.New()
	store := testSendFixture(ws, id, stepID)
	boom := errors.New("redis unavailable")
	svc := campaign.NewService(store, noopChecker{},
		campaign.WithTestSendEnqueuer(&fakeTestSendEnqueuer{err: boom}), campaign.WithRateLimiter(&fakeRateLimiter{allow: true}))

	if err := svc.TestSend(ctx, ws, id, stepID, "preview@example.com"); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the propagated enqueue error", err)
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost,
		"/campaigns/"+campaignID.String()+"/test-send", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)

	root := chi.NewRouter()
	root.Mount("/campaigns", h.Routes(alwaysVerifiedChecker{}))

	w := httptest.NewRecorder()
	auth.RequireAuth(auth.NewJWTVerifier(secret))(root).ServeHTTP(w, req)
	return w
}

func TestTestSendHandlerReturns202AndQueuedTrue(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	ws, id, stepID := uuid.New(), uuid.New(), uuid.New()
	store := testSendFixture(ws, id, stepID)
	enq := &fakeTestSendEnqueuer{}
	h := campaign.NewHandler(campaign.NewService(store, noopChecker{},
		campaign.WithTestSendEnqueuer(enq), campaign.WithRateLimiter(&fakeRateLimiter{allow: true})),
		noopEnqueuer{})

	body := `{"step_id":"` + stepID.String() + `","to":"preview@example.com"}`
	w := serveTestSend(t, h, secret, ws, id, body)

	if w.Code != http.StatusAccepted {
		t.Fatalf("code = %d, want 202: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"queued":true`) {
		t.Errorf("body = %s, want {\"queued\":true}", w.Body.String())
	}
	// The request reached the enqueue seam -- and, by construction (there is no
	// Mailer/SenderResolver left in this package, see the file-level comment),
	// nothing was decrypted or dialed to produce this response.
	if len(enq.calls) != 1 {
		t.Fatalf("enqueue calls = %d, want 1", len(enq.calls))
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
				campaign.WithTestSendEnqueuer(&fakeTestSendEnqueuer{}), campaign.WithRateLimiter(&fakeRateLimiter{allow: true})),
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
		campaign.WithTestSendEnqueuer(&fakeTestSendEnqueuer{}), campaign.WithRateLimiter(&fakeRateLimiter{allow: true})),
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
		campaign.WithTestSendEnqueuer(&fakeTestSendEnqueuer{}), campaign.WithRateLimiter(&fakeRateLimiter{allow: true})),
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
