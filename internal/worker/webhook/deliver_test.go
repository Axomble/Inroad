package webhook

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/queue"
	"github.com/inroad/inroad/internal/platform/webhookwire"
)

type fakeCore struct {
	job    coreapi.WebhookDeliveryJob
	jobErr error

	deliveredAttempts int
	deliveredStatus   int
	deliveredCalled   bool

	retryAttempts int
	retryErr      string
	retryStatus   *int
	retryNextAt   time.Time
	retryCalled   bool

	failAttempts int
	failErr      string
	failStatus   *int
	failCalled   bool
}

func (f *fakeCore) GetWebhookDeliveryJob(context.Context, string, string) (coreapi.WebhookDeliveryJob, error) {
	return f.job, f.jobErr
}

func (f *fakeCore) MarkWebhookDelivered(_ context.Context, _, _ string, attempts, responseStatus int) error {
	f.deliveredCalled, f.deliveredAttempts, f.deliveredStatus = true, attempts, responseStatus
	return nil
}

func (f *fakeCore) MarkWebhookRetrying(_ context.Context, _, _ string, attempts int, lastErr string, responseStatus *int, nextAttemptAt time.Time) error {
	f.retryCalled, f.retryAttempts, f.retryErr, f.retryStatus, f.retryNextAt = true, attempts, lastErr, responseStatus, nextAttemptAt
	return nil
}

func (f *fakeCore) MarkWebhookFailed(_ context.Context, _, _ string, attempts int, lastErr string, responseStatus *int) error {
	f.failCalled, f.failAttempts, f.failErr, f.failStatus = true, attempts, lastErr, responseStatus
	return nil
}

type fakeEnq struct {
	deliveryID string
	delay      time.Duration
	calls      int32
}

func (f *fakeEnq) EnqueueWebhookDeliverIn(_ context.Context, deliveryID, _ string, d time.Duration) error {
	atomic.AddInt32(&f.calls, 1)
	f.deliveryID, f.delay = deliveryID, d
	return nil
}

// testClient mirrors DeliverHandler's client but with allowPrivate so httptest's
// loopback server is reachable.
func testClient() *http.Client {
	return &http.Client{
		Timeout:       postTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport:     &http.Transport{DialContext: mail.GuardedDialContext(true)},
	}
}

func baseJob(url string) coreapi.WebhookDeliveryJob {
	return coreapi.WebhookDeliveryJob{
		DeliveryID:     "11111111-1111-1111-1111-111111111111",
		EndpointID:     "22222222-2222-2222-2222-222222222222",
		WorkspaceID:    "33333333-3333-3333-3333-333333333333",
		EventType:      "ping",
		Payload:        []byte(`{"id":"d","event":"ping","occurred_at":"2026-09-07T00:00:00Z","data":{}}`),
		Secret:         []byte("whsec_test"),
		URL:            url,
		Attempts:       0,
		Status:         "pending",
		EndpointActive: true,
	}
}

func TestDeliver2xxMarksDelivered(t *testing.T) {
	var gotSig, gotEvent, gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSig = r.Header.Get(webhookwire.SignatureHeader)
		gotEvent = r.Header.Get("Inroad-Event")
		gotUA = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	core := &fakeCore{job: baseJob(srv.URL)}
	enq := &fakeEnq{}
	// Capture the secret + body before deliver runs: the handler zeroizes the
	// secret slice after signing (shared backing array), which is correct.
	wantSecret := append([]byte(nil), core.job.Secret...)
	wantBody := append([]byte(nil), core.job.Payload...)
	if err := deliver(context.Background(), core, enq, testClient(), true, queue.WebhookDeliverPayload{DeliveryID: "d", WorkspaceID: "w"}); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if !core.deliveredCalled || core.deliveredAttempts != 1 || core.deliveredStatus != 200 {
		t.Fatalf("delivered call = %+v", core)
	}
	if core.retryCalled || core.failCalled || enq.calls != 0 {
		t.Fatal("a 2xx must not retry or fail")
	}
	if gotUA != "Inroad-Webhooks/1" || gotEvent != "ping" {
		t.Fatalf("headers: UA=%q event=%q", gotUA, gotEvent)
	}
	if !webhookwire.Verify(wantSecret, wantBody, gotSig) {
		t.Fatalf("receiver could not verify the signature %q", gotSig)
	}
}

func TestDeliver500RetriesWithBackoff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	core := &fakeCore{job: baseJob(srv.URL)} // Attempts: 0 -> this is attempt 1
	enq := &fakeEnq{}
	if err := deliver(context.Background(), core, enq, testClient(), true, queue.WebhookDeliverPayload{}); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if !core.retryCalled || core.retryAttempts != 1 {
		t.Fatalf("expected retry at attempt 1, got %+v", core)
	}
	if core.retryStatus == nil || *core.retryStatus != 500 {
		t.Fatalf("retry response_status = %v, want 500", core.retryStatus)
	}
	if enq.calls != 1 || enq.delay != backoff[0] {
		t.Fatalf("re-enqueue delay = %v, want %v (calls=%d)", enq.delay, backoff[0], enq.calls)
	}

	// Attempt 3 failing uses backoff[2] = 30m.
	core = &fakeCore{job: func() coreapi.WebhookDeliveryJob { j := baseJob(srv.URL); j.Attempts = 2; return j }()}
	enq = &fakeEnq{}
	if err := deliver(context.Background(), core, enq, testClient(), true, queue.WebhookDeliverPayload{}); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if core.retryAttempts != 3 || enq.delay != backoff[2] {
		t.Fatalf("attempt 3: retryAttempts=%d delay=%v, want 3 / %v", core.retryAttempts, enq.delay, backoff[2])
	}
}

func TestDeliverSixthFailureMarksFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	// Attempts already 5 -> this is attempt 6 -> terminal.
	core := &fakeCore{job: func() coreapi.WebhookDeliveryJob { j := baseJob(srv.URL); j.Attempts = 5; return j }()}
	enq := &fakeEnq{}
	if err := deliver(context.Background(), core, enq, testClient(), true, queue.WebhookDeliverPayload{}); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if !core.failCalled || core.failAttempts != 6 {
		t.Fatalf("expected failed at attempt 6, got %+v", core)
	}
	if core.retryCalled || enq.calls != 0 {
		t.Fatal("the 6th failure must not re-enqueue")
	}
}

func TestDeliverSSRFRecheckBlocksRebind(t *testing.T) {
	// The endpoint URL now resolves to loopback; the worker runs with
	// allowPrivate=false, so the pre-dial VetURL rejects it and the delivery is
	// failed without a POST.
	core := &fakeCore{job: baseJob("http://127.0.0.1:9/hook")}
	enq := &fakeEnq{}
	if err := deliver(context.Background(), core, enq, testClient(), false, queue.WebhookDeliverPayload{}); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if !core.failCalled {
		t.Fatal("an SSRF-blocked target must be marked failed")
	}
	if core.retryCalled || enq.calls != 0 {
		t.Fatal("an SSRF block is terminal, not retryable")
	}
}

func TestDeliverNonPendingIsNoOp(t *testing.T) {
	core := &fakeCore{job: func() coreapi.WebhookDeliveryJob {
		j := baseJob("https://8.8.8.8/hook")
		j.Status = "delivered"
		return j
	}()}
	enq := &fakeEnq{}
	if err := deliver(context.Background(), core, enq, testClient(), true, queue.WebhookDeliverPayload{}); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if core.deliveredCalled || core.retryCalled || core.failCalled || enq.calls != 0 {
		t.Fatalf("a non-pending row must be a pure no-op, got %+v", core)
	}
}

func TestDeliverInactiveEndpointFailsWithoutDialing(t *testing.T) {
	core := &fakeCore{job: func() coreapi.WebhookDeliveryJob {
		j := baseJob("https://8.8.8.8/hook")
		j.EndpointActive = false
		return j
	}()}
	enq := &fakeEnq{}
	if err := deliver(context.Background(), core, enq, testClient(), true, queue.WebhookDeliverPayload{}); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if !core.failCalled || core.retryCalled {
		t.Fatalf("inactive endpoint should fail terminally, got %+v", core)
	}
}
