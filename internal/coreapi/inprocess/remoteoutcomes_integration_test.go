//go:build integration

package inprocess

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/coreapi/remote"
	"github.com/inroad/inroad/internal/platform/credbroker"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/queue"
	"github.com/inroad/inroad/internal/worker/sequence"
)

// Slice 3 of the coreapi remote transport, end to end: the CLAIM AND OUTCOME
// path, against real Postgres, through the real handler, from a worker-side
// client that HAS NO DATABASE — and, for the tests this slice exists for, with
// the response DROPPED after the control plane committed.
//
// # Why this file drives the real send handler
//
// The other remote integration files call coreapi methods directly, which is the
// right shape for a read: the question is "does the answer cross". A write's
// question is different and cannot be asked one method at a time. It is "when the
// answer to a write is lost, what does the RETRY do" — and the retry is
// internal/worker/sequence.AdvanceHandler re-running from the top, through
// GetStepSendJob, the claim, the send and the finalize. So the handler is what
// runs here, with a counting Sender standing in for SMTP.
//
// asynq is deliberately absent. Its retry IS a re-invocation of the same handler
// with the same payload; calling the handler twice is that, without needing a
// Redis in the test.

// ---------------------------------------------------------------------------
// The fault injector
// ---------------------------------------------------------------------------

// responseDropper sits between the worker and the control plane and can lose a
// response AFTER the control plane has committed.
//
// drop(path) runs the REAL handler to completion against a throwaway recorder —
// so everything it wrote is written and everything it committed is committed —
// and then hijacks the connection and closes it without sending a byte. The
// worker sees a transport failure it cannot distinguish from a call that never
// happened, which is exactly the third outcome an in-process call does not have.
//
// refuse(path) is the other half of the same uncertainty: the request never
// reaches the handler at all, so nothing was committed. The pair is what lets a
// test say "the delivery landed and the advance never ran" precisely rather than
// approximately.
//
// Every field below is touched by TWO goroutines: the test sets the predicates
// and reads the counters, while net/http runs ServeHTTP on its own connection
// goroutine. They are therefore all guarded. This is not defensive style — an
// unguarded version passed locally and failed under `go test -race`, which is
// how CI runs the integration suite.
type responseDropper struct {
	inner http.Handler

	mu      sync.Mutex
	drop    func(path string) bool
	refuse  func(path string) bool
	dropped int
	refused int
}

// setDrop / setRefuse install a predicate; counts() reads the tallies. The test
// goroutine must go through these rather than touching the fields directly.
func (d *responseDropper) setDrop(fn func(path string) bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.drop = fn
}

func (d *responseDropper) setRefuse(fn func(path string) bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.refuse = fn
}

func (d *responseDropper) counts() (dropped, refused int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.dropped, d.refused
}

func (d *responseDropper) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	refuse, drop := d.refuse, d.drop
	d.mu.Unlock()

	switch {
	case refuse != nil && refuse(r.URL.Path):
		d.mu.Lock()
		d.refused++
		d.mu.Unlock()
		// A 500 with no body: the control plane was unreachable for this call.
		w.WriteHeader(http.StatusInternalServerError)
	case drop != nil && drop(r.URL.Path):
		d.inner.ServeHTTP(httptest.NewRecorder(), r)
		d.mu.Lock()
		d.dropped++
		d.mu.Unlock()
		hj, ok := w.(http.Hijacker)
		if !ok {
			// httptest.NewServer is HTTP/1.1, which always supports hijacking.
			// If that ever stops being true this test is silently no longer
			// testing anything, so it must fail loudly rather than pass.
			panic("the test server does not support hijacking; the response cannot be dropped")
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			panic("hijack failed, so the response was not dropped: " + err.Error())
		}
		_ = conn.Close()
	default:
		d.inner.ServeHTTP(w, r)
	}
}

// never is the "let everything through" predicate, so a test names only the
// paths it means to break.
func never(string) bool { return false }

// once returns a predicate that matches `path` exactly once. That is the shape
// every scenario here needs: the first attempt is broken, the retry must not be.
func once(path string) func(string) bool {
	fired := false
	return func(p string) bool {
		if p == path && !fired {
			fired = true
			return true
		}
		return false
	}
}

// faultyFleet stands the real fleet handler up behind a responseDropper and
// returns a worker-side coreapi client pointed at it, plus the dropper so a test
// can reconfigure it between attempts and assert it actually fired.
func faultyFleet(t *testing.T, f poolFixture) (coreapi.Client, *responseDropper) {
	t.Helper()
	upstream := fleetListener(t, f.q, itKeyring(t, f.q), f.core)
	dropper := &responseDropper{
		inner: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Forward to the real listener as an ordinary reverse hop. It is a
			// separate httptest server rather than the handler value because
			// fleetListener owns its own mux and lifecycle.
			proxyTo(t, upstream.URL, w, r)
		}),
		drop:   never,
		refuse: never,
	}
	srv := httptest.NewServer(dropper)
	t.Cleanup(srv.Close)
	return remoteOutcomeCore(t, srv.URL), dropper
}

// proxyTo forwards one request to the real fleet listener and copies the answer
// back verbatim. Nothing is interpreted: the dropper decides whether the caller
// ever sees it.
func proxyTo(t *testing.T, baseURL string, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	req, err := http.NewRequestWithContext(r.Context(), r.Method, baseURL+r.URL.Path, r.Body)
	if err != nil {
		t.Errorf("proxy build request: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	req.Header = r.Header.Clone()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Errorf("proxy: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// remoteOutcomeCore builds the EXECUTION plane's coreapi client the way
// cmd/worker does for a role=send fleet host: NO POOL, NO KEYRING, a credential
// broker, and all FOUR remote source seams installed.
//
// The nil pool is the assertion, not a shortcut: localOutcomes would dereference
// it on the first Begin, so every row this client moves demonstrably moved over
// the wire.
func remoteOutcomeCore(t *testing.T, baseURL string) coreapi.Client {
	t.Helper()
	opener, err := credbroker.NewHTTPOpener(baseURL, remoteTestToken, true) // httptest speaks http
	if err != nil {
		t.Fatalf("credbroker.NewHTTPOpener: %v", err)
	}
	rc, err := remote.NewClient(baseURL, remoteTestToken, true, opener)
	if err != nil {
		t.Fatalf("remote.NewClient: %v", err)
	}
	return New(nil, nil, nil, "", mail.GoogleOAuth{}, mail.MicrosoftOAuth{}, nil, nil,
		WithCredentialBroker(opener), WithRemoteSuppression(rc),
		WithRemoteJobs(rc), WithRemoteOutcomes(rc), WithRemoteInboxSends(rc))
}

// ---------------------------------------------------------------------------
// The send harness
// ---------------------------------------------------------------------------

// countingSender is the SMTP leg. Its only job is to count: every assertion in
// this file and in remoteinboxsends_integration_test.go about a double send is an
// assertion about this number.
//
// failNext makes the next N attempts fail BEFORE the count moves, which is what a
// provider rejection looks like to a handler: nothing left this process, so the
// release-and-retry path is exercised without inventing a send that did not
// happen.
type countingSender struct {
	sends    int
	failNext int
	lastMsg  string
}

var errSenderRejected = errors.New("the provider rejected the message")

func (s *countingSender) Send(_ context.Context, _ mail.OutboundJob, _ mail.Message) (string, error) {
	if s.failNext > 0 {
		s.failNext--
		return "", errSenderRejected
	}
	s.sends++
	s.lastMsg = "<sent-" + uuid.NewString() + "@acme.test>"
	return s.lastMsg, nil
}

// recordingEnqueuer stands in for Redis.
type recordingEnqueuer struct{ advances, evaluations int }

func (e *recordingEnqueuer) EnqueueAdvanceAt(context.Context, string, string, time.Time) error {
	e.advances++
	return nil
}

func (e *recordingEnqueuer) EnqueueAdvanceIn(context.Context, string, string, time.Duration) error {
	e.advances++
	return nil
}

func (e *recordingEnqueuer) EnqueueDeliverabilityEvaluate(context.Context, string, string) error {
	e.evaluations++
	return nil
}

// advanceOnce runs the sequence:advance handler exactly the way asynq delivers
// it: one task, one payload, one invocation. Calling it twice IS the retry.
func advanceOnce(t *testing.T, ctx context.Context, core coreapi.Client, s *countingSender,
	enq *recordingEnqueuer, enrollmentID, workspaceID string,
) error {
	t.Helper()
	payload, err := json.Marshal(queue.AdvancePayload{EnrollmentID: enrollmentID, WorkspaceID: workspaceID})
	if err != nil {
		t.Fatalf("payload: %v", err)
	}
	h := sequence.AdvanceHandler(core, s, enq, "https://app.test", []byte("tracking-secret"), nil)
	return h(ctx, asynq.NewTask(queue.TaskSequenceAdvance, payload))
}

// sendRows reports how many sends rows exist for this enrollment's campaign and
// contact, and the status of the one this step claimed. Two rows would BE the
// double send; a second delivery against one row would show as a second call to
// countingSender.
func sendState(t *testing.T, ctx context.Context, f poolFixture, campaignID uuid.UUID) (rows int, status string) {
	t.Helper()
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*), coalesce(max(status), '') FROM sends WHERE campaign_id = $1 AND workspace_id = $2`,
		campaignID, f.ws).Scan(&rows, &status); err != nil {
		t.Fatalf("read sends: %v", err)
	}
	return rows, status
}

func currentStep(t *testing.T, ctx context.Context, f poolFixture, enrollmentID uuid.UUID) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(ctx,
		`SELECT current_step FROM sequence_enrollments WHERE id = $1 AND workspace_id = $2`,
		enrollmentID, f.ws).Scan(&n); err != nil {
		t.Fatalf("read cursor: %v", err)
	}
	return n
}

// ---------------------------------------------------------------------------
// The tests this slice exists for
// ---------------------------------------------------------------------------

// The happy path first, so every scenario below is a deviation from something
// that demonstrably works. A worker with no database claims, sends, records the
// delivery and advances the cursor — all four over the wire.
func TestARemoteSendClaimsDeliversAndAdvancesWithNoPool(t *testing.T) {
	ctx, f := setupPool(t)
	enrollmentID := f.enroll(t, ctx)
	worker, _ := faultyFleet(t, f)
	s, enq := &countingSender{}, &recordingEnqueuer{}

	if err := advanceOnce(t, ctx, worker, s, enq, enrollmentID.String(), f.ws.String()); err != nil {
		t.Fatalf("advance: %v", err)
	}
	if s.sends != 1 {
		t.Fatalf("sent %d times, want 1", s.sends)
	}
	rows, status := sendState(t, ctx, f, f.campaignID)
	if rows != 1 || status != "sent" {
		t.Errorf("sends = %d rows, status %q; want one 'sent' row", rows, status)
	}
	if got := currentStep(t, ctx, f, enrollmentID); got != 1 {
		t.Errorf("current_step = %d, want 1", got)
	}
}

// SCENARIO 1 — the CLAIM commits and its response is lost.
//
// The worker never learns it holds the claim, so it never sends. The retry must
// not take a second claim on the same row (which, on a row it did not know it
// owned, would be a send the protocol never authorised) — it must be told the
// row is already 'sending' and stop.
//
// This is the case that is UNREACHABLE in process: a function call cannot commit
// and then fail to return.
func TestALostClaimResponseNeverBecomesASecondClaim(t *testing.T) {
	ctx, f := setupPool(t)
	enrollmentID := f.enroll(t, ctx)
	worker, dropper := faultyFleet(t, f)
	s, enq := &countingSender{}, &recordingEnqueuer{}

	dropper.setDrop(once(remote.PathStepSendClaim))
	if err := advanceOnce(t, ctx, worker, s, enq, enrollmentID.String(), f.ws.String()); err == nil {
		t.Fatal("the attempt whose claim response was lost reported success")
	}
	if dropped, _ := dropper.counts(); dropped != 1 {
		t.Fatalf("the injector dropped %d responses, want 1 — this test proved nothing", dropped)
	}
	if s.sends != 0 {
		t.Fatalf("sent %d times on an attempt that never learned it held the claim, want 0", s.sends)
	}
	// The control plane DID commit the claim. Without this the scenario is just a
	// failed call.
	rows, status := sendState(t, ctx, f, f.campaignID)
	if rows != 1 || status != "sending" {
		t.Fatalf("sends = %d rows, status %q; want one 'sending' row the control plane committed", rows, status)
	}

	// The retry.
	if err := advanceOnce(t, ctx, worker, s, enq, enrollmentID.String(), f.ws.String()); err != nil {
		t.Fatalf("the retry failed: %v", err)
	}
	if s.sends != 0 {
		t.Errorf("the retry sent %d times against a claim it already held, want 0", s.sends)
	}
	rows, status = sendState(t, ctx, f, f.campaignID)
	if rows != 1 {
		t.Errorf("sends = %d rows after the retry, want 1 — a second row is the double claim", rows)
	}
	if status != "sending" {
		t.Errorf("status = %q, want the lease left in place for the sweeper", status)
	}
}

// SCENARIO 2 — THE double send test. The send went out, the DELIVERY was
// recorded, and the response was lost.
//
// The worker returns an error, asynq retries the whole task, and the retry's
// claim sees a 'sent' row: ClaimAlreadySent, recover forward, do not re-send.
// Exactly one message leaves this process.
//
// If this assertion ever reads 2, the fleet has double-sent a customer's mail.
func TestALostDeliveryResponseNeverBecomesADoubleSend(t *testing.T) {
	ctx, f := setupPool(t)
	enrollmentID := f.enroll(t, ctx)
	worker, dropper := faultyFleet(t, f)
	s, enq := &countingSender{}, &recordingEnqueuer{}

	dropper.setDrop(once(remote.PathStepSendDelivered))
	if err := advanceOnce(t, ctx, worker, s, enq, enrollmentID.String(), f.ws.String()); err == nil {
		t.Fatal("the attempt whose delivery response was lost reported success")
	}
	if dropped, _ := dropper.counts(); dropped != 1 {
		t.Fatalf("the injector dropped %d responses, want 1", dropped)
	}
	if s.sends != 1 {
		t.Fatalf("sent %d times, want 1 — the message did go out on this attempt", s.sends)
	}
	// The delivery IS recorded. This is what makes the retry dangerous and the
	// claim necessary.
	rows, status := sendState(t, ctx, f, f.campaignID)
	if rows != 1 || status != "sent" {
		t.Fatalf("sends = %d rows, status %q; want one 'sent' row the control plane committed", rows, status)
	}
	// The cursor did NOT advance: the handler returned before AdvanceStepCursor.
	// That is the state recover-forward exists to repair.
	if got := currentStep(t, ctx, f, enrollmentID); got != 0 {
		t.Fatalf("current_step = %d before the retry, want 0", got)
	}

	// The retry.
	if err := advanceOnce(t, ctx, worker, s, enq, enrollmentID.String(), f.ws.String()); err != nil {
		t.Fatalf("the retry failed: %v", err)
	}
	if s.sends != 1 {
		t.Fatalf("THE DOUBLE SEND: sent %d times for one step, want exactly 1", s.sends)
	}
	if rows, _ := sendState(t, ctx, f, f.campaignID); rows != 1 {
		t.Errorf("sends = %d rows, want 1", rows)
	}
	if got := currentStep(t, ctx, f, enrollmentID); got != 1 {
		t.Errorf("current_step = %d after recover-forward, want 1", got)
	}
}

// SCENARIO 3 — the ordering rule itself: MarkStepDelivered commits and
// AdvanceStepCursor NEVER RUNS.
//
// This is why the two are separate committed steps rather than one transaction.
// The retry re-claims, is told the step is already sent, and runs the cursor
// advance alone.
func TestADeliveryWhoseAdvanceNeverRanRecoversForward(t *testing.T) {
	ctx, f := setupPool(t)
	enrollmentID := f.enroll(t, ctx)
	worker, dropper := faultyFleet(t, f)
	s, enq := &countingSender{}, &recordingEnqueuer{}

	dropper.setRefuse(once(remote.PathStepSendAdvance))
	if err := advanceOnce(t, ctx, worker, s, enq, enrollmentID.String(), f.ws.String()); err == nil {
		t.Fatal("the attempt whose cursor advance never ran reported success")
	}
	if _, refused := dropper.counts(); refused != 1 {
		t.Fatalf("the injector refused %d calls, want 1", refused)
	}
	if s.sends != 1 {
		t.Fatalf("sent %d times, want 1", s.sends)
	}
	if _, status := sendState(t, ctx, f, f.campaignID); status != "sent" {
		t.Fatalf("status = %q, want 'sent' — the delivery commits BEFORE the advance", status)
	}
	if got := currentStep(t, ctx, f, enrollmentID); got != 0 {
		t.Fatalf("current_step = %d, want 0 — the advance never ran", got)
	}

	// The retry recovers forward: cursor only, no second send.
	if err := advanceOnce(t, ctx, worker, s, enq, enrollmentID.String(), f.ws.String()); err != nil {
		t.Fatalf("the retry failed: %v", err)
	}
	if s.sends != 1 {
		t.Fatalf("THE DOUBLE SEND: sent %d times for one step, want exactly 1", s.sends)
	}
	if got := currentStep(t, ctx, f, enrollmentID); got != 1 {
		t.Errorf("current_step = %d, want 1 — recover-forward did not advance", got)
	}
}

// SCENARIO 3b — the advance's response lost AFTER it committed, which is a
// DIFFERENT case from 3 and is worth stating precisely, because getting it wrong
// is how this test was briefly nondeterministic.
//
// Once the advance has committed there is nothing left to recover forward: the
// enrollment is at step 1, so the retried asynq task's GetStepSendJob answers
// with STEP 2 and the task moves on to the next unit of work. With setupPool's
// default zero delay, step 2 is due the instant step 1 lands, so the retry
// legitimately sends it — and a bare "exactly one send" assertion then passes or
// fails on whether the wall clock happened to be inside the campaign's send
// window.
//
// That is in-process behaviour, not something this transport introduced: an
// in-process worker that committed the advance and then died before its enqueue
// is redelivered the same task and does exactly the same thing. No message is
// duplicated either way — step 1's row is written once and step 2 is a different
// message.
//
// So the delay is lengthened, which makes the retry's step-2 job NOT YET DUE and
// turns the send count back into an exact statement about step 1. The retry then
// never reaches the claim at all, which is why the cursor's idempotence is
// asserted DIRECTLY below rather than inferred from the handler: calling
// AdvanceStepCursor again over the wire is what an incrementing cursor would
// fail, and nothing else here would catch it.
func TestALostAdvanceResponseLeavesACursorARepeatCannotMove(t *testing.T) {
	ctx, f := setupPool(t)
	if _, err := f.pool.Exec(ctx,
		`UPDATE sequence_steps SET delay_seconds = 86400 WHERE campaign_id = $1 AND workspace_id = $2 AND step_order = 2`,
		f.campaignID, f.ws); err != nil {
		t.Fatalf("lengthen step 2's delay: %v", err)
	}
	enrollmentID := f.enroll(t, ctx)
	worker, dropper := faultyFleet(t, f)
	s, enq := &countingSender{}, &recordingEnqueuer{}

	job, err := worker.GetStepSendJob(ctx, enrollmentID.String(), f.ws.String())
	if err != nil {
		t.Fatalf("GetStepSendJob: %v", err)
	}

	dropper.setDrop(once(remote.PathStepSendAdvance))
	if err := advanceOnce(t, ctx, worker, s, enq, enrollmentID.String(), f.ws.String()); err == nil {
		t.Fatal("the attempt whose advance response was lost reported success")
	}
	if dropped, _ := dropper.counts(); dropped != 1 {
		t.Fatalf("the injector dropped %d responses, want 1", dropped)
	}
	// The advance DID commit on the control plane.
	if got := currentStep(t, ctx, f, enrollmentID); got != 1 {
		t.Fatalf("current_step = %d, want 1 — the advance committed before its answer was lost", got)
	}

	// The task retry: step 2 is not due, so it defers without sending.
	if err := advanceOnce(t, ctx, worker, s, enq, enrollmentID.String(), f.ws.String()); err != nil {
		t.Fatalf("the retry failed: %v", err)
	}
	if s.sends != 1 {
		t.Fatalf("THE DOUBLE SEND: sent %d times for one step, want exactly 1", s.sends)
	}
	if rows, _ := sendState(t, ctx, f, f.campaignID); rows != 1 {
		t.Errorf("sends = %d rows, want 1 — a second row means a step went out on the retry", rows)
	}

	// And the method's own idempotence, driven directly: re-running the advance
	// the worker never learned had committed lands on the SAME cursor. An
	// incrementing advance would read 2 or 3 here and skip step 2 of the
	// sequence entirely.
	for i := range 2 {
		if _, err := worker.AdvanceStepCursor(ctx, job); err != nil {
			t.Fatalf("repeated AdvanceStepCursor %d: %v", i+1, err)
		}
		if got := currentStep(t, ctx, f, enrollmentID); got != 1 {
			t.Fatalf("current_step = %d after repeat %d, want 1 — the cursor is set absolutely", got, i+1)
		}
	}
}

// Repeating a delivery directly is a no-op returning the same answer, not a
// second write and not an error. The claim means the send handler never does
// this, but the method's contract is idempotence by key, so it is asserted at
// the method rather than inferred from the caller.
func TestARepeatedRemoteDeliveryIsANoOp(t *testing.T) {
	ctx, f := setupPool(t)
	enrollmentID := f.enroll(t, ctx)
	worker, _ := faultyFleet(t, f)

	job, err := worker.GetStepSendJob(ctx, enrollmentID.String(), f.ws.String())
	if err != nil {
		t.Fatalf("GetStepSendJob: %v", err)
	}
	if outcome, err := worker.ClaimStepSend(ctx, job); err != nil || outcome != coreapi.ClaimWon {
		t.Fatalf("ClaimStepSend = %v, %v; want ClaimWon", outcome, err)
	}
	const msgID = "<once@acme.test>"
	for i := range 3 {
		if err := worker.MarkStepDelivered(ctx, job, msgID); err != nil {
			t.Fatalf("MarkStepDelivered attempt %d: %v", i+1, err)
		}
	}
	rows, status := sendState(t, ctx, f, f.campaignID)
	if rows != 1 || status != "sent" {
		t.Errorf("sends = %d rows, status %q; want one 'sent' row after three deliveries", rows, status)
	}
	var stored string
	if err := f.pool.QueryRow(ctx,
		`SELECT message_id FROM sends WHERE campaign_id = $1 AND workspace_id = $2`,
		f.campaignID, f.ws).Scan(&stored); err != nil {
		t.Fatalf("read message id: %v", err)
	}
	if stored != msgID {
		t.Errorf("message_id = %q, want %q unchanged by the repeats", stored, msgID)
	}
}

// Fail closed where it matters: the enrollment IS in Postgres and the control
// plane is gone. Every outcome must return an error — never a success, never a
// claim it did not take.
func TestEveryOutcomeFailsClosedWhenTheControlPlaneDies(t *testing.T) {
	ctx, f := setupPool(t)
	enrollmentID := f.enroll(t, ctx)
	upstream := fleetListener(t, f.q, itKeyring(t, f.q), f.core)
	worker := remoteOutcomeCore(t, upstream.URL)

	job, err := worker.GetStepSendJob(ctx, enrollmentID.String(), f.ws.String())
	if err != nil {
		t.Fatalf("GetStepSendJob while the control plane was up: %v", err)
	}
	upstream.Close()

	if outcome, err := worker.ClaimStepSend(ctx, job); err == nil {
		t.Error("ClaimStepSend answered with the control plane down")
	} else if outcome != coreapi.ClaimSkip {
		t.Errorf("ClaimStepSend = %v alongside its error, want the fail-closed ClaimSkip", outcome)
	}
	if err := worker.MarkStepDelivered(ctx, job, "<m@x>"); err == nil {
		t.Error("MarkStepDelivered answered with the control plane down")
	}
	if adv, err := worker.AdvanceStepCursor(ctx, job); err == nil {
		t.Error("AdvanceStepCursor answered with the control plane down")
	} else if !reflect.ValueOf(adv).IsZero() {
		t.Errorf("AdvanceStepCursor returned %+v alongside its error", adv)
	}
	if err := worker.MarkStepStopped(ctx, enrollmentID.String(), f.ws.String(), "suppressed"); err == nil {
		t.Error("MarkStepStopped answered with the control plane down")
	}
	// And nothing moved.
	if rows, _ := sendState(t, ctx, f, f.campaignID); rows != 0 {
		t.Errorf("sends = %d rows after a run against a dead control plane, want 0", rows)
	}
	if got := currentStep(t, ctx, f, enrollmentID); got != 0 {
		t.Errorf("current_step = %d, want 0", got)
	}
}

// Workspace-pinned across the wire: the control plane applies the same
// workspace_id filter the in-process path applies, so a foreign workspace claims,
// delivers and advances ZERO rows (docs/security.md invariant 4).
func TestOutcomeWritesArePinnedToTheRequestedWorkspace(t *testing.T) {
	ctx, f := setupPool(t)
	enrollmentID := f.enroll(t, ctx)
	worker, _ := faultyFleet(t, f)

	job, err := worker.GetStepSendJob(ctx, enrollmentID.String(), f.ws.String())
	if err != nil {
		t.Fatalf("GetStepSendJob: %v", err)
	}
	// Re-point the job at another tenant, which is the most a compromised worker
	// could do: the envelope and the job agree, so the request is well-formed and
	// the pin has to be the SQL filter rather than a shape check.
	foreign := job
	foreign.WorkspaceID = f.foreignWS.String()

	// Each call may be REFUSED (a composite tenant foreign key rejects the write)
	// or may SUCCEED against zero rows (a workspace-pinned WHERE matches nothing).
	// Which one it is depends on the statement and is not the property under test;
	// what must hold either way is that the owning workspace is untouched. The
	// errors are therefore deliberately not asserted on, only the rows.
	_, _ = worker.ClaimStepSend(ctx, foreign)
	if rows, _ := sendState(t, ctx, f, f.campaignID); rows != 0 {
		t.Errorf("a foreign-workspace claim wrote %d rows into the owning workspace", rows)
	}
	_ = worker.MarkStepDelivered(ctx, foreign, "<x@y>")
	if rows, _ := sendState(t, ctx, f, f.campaignID); rows != 0 {
		t.Errorf("a foreign-workspace delivery wrote %d rows into the owning workspace", rows)
	}
	_, _ = worker.AdvanceStepCursor(ctx, foreign)
	if got := currentStep(t, ctx, f, enrollmentID); got != 0 {
		t.Errorf("a foreign-workspace advance moved the cursor to %d, want 0", got)
	}
	_ = worker.MarkStepStopped(ctx, enrollmentID.String(), f.foreignWS.String(), "suppressed")
	var status string
	if err := f.pool.QueryRow(ctx,
		`SELECT status FROM sequence_enrollments WHERE id = $1 AND workspace_id = $2`,
		enrollmentID, f.ws).Scan(&status); err != nil {
		t.Fatalf("read enrollment status: %v", err)
	}
	if status != "active" {
		t.Errorf("a foreign-workspace stop moved the enrollment to %q, want it left active", status)
	}
}

// The compliance outcomes, over the wire, asserted on the suppression table the
// control plane owns. MarkUnsubscribed's suppression is the load-bearing write of
// docs/security.md invariant 20 and must survive a transport change.
func TestRemoteComplianceOutcomesSuppressTheAddress(t *testing.T) {
	ctx, f := setupPool(t)
	worker, _ := faultyFleet(t, f)
	local := f.core.(suppressionCapability)

	unsub := "unsub-" + uuid.NewString() + "@x.test"
	bounced := "bounce-" + uuid.NewString() + "@x.test"

	// An empty enrollment id is the legacy direct-send path and must work: the
	// address is suppressed even though there is nothing to stop.
	if err := worker.MarkUnsubscribed(ctx, "", f.ws.String(), unsub); err != nil {
		t.Fatalf("MarkUnsubscribed: %v", err)
	}
	if err := worker.MarkBounced(ctx, "", f.ws.String(), bounced, true); err != nil {
		t.Fatalf("MarkBounced: %v", err)
	}
	for _, email := range []string{unsub, bounced} {
		got, err := local.IsSuppressed(ctx, f.ws.String(), email)
		if err != nil {
			t.Fatalf("IsSuppressed(%s): %v", email, err)
		}
		if !got {
			t.Errorf("%s is not suppressed; the outcome did not reach the table", email)
		}
	}
	// Both are idempotent: repeating them writes nothing new and errors on
	// nothing.
	if err := worker.MarkUnsubscribed(ctx, "", f.ws.String(), unsub); err != nil {
		t.Errorf("a repeated MarkUnsubscribed failed: %v", err)
	}
	if err := worker.MarkBounced(ctx, "", f.ws.String(), bounced, true); err != nil {
		t.Errorf("a repeated MarkBounced failed: %v", err)
	}
	var n int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM suppression WHERE workspace_id = $1 AND email IN ($2, $3)`,
		f.ws, unsub, bounced).Scan(&n); err != nil {
		t.Fatalf("count suppressions: %v", err)
	}
	if n != 2 {
		t.Errorf("suppressions = %d rows after repeating both outcomes, want 2", n)
	}
	// A soft bounce is a no-op on BOTH transports: hard=false travels rather
	// than being short-circuited client-side, and the control plane declines it.
	soft := "soft-" + uuid.NewString() + "@x.test"
	if err := worker.MarkBounced(ctx, "", f.ws.String(), soft, false); err != nil {
		t.Fatalf("MarkBounced(hard=false): %v", err)
	}
	if got, err := local.IsSuppressed(ctx, f.ws.String(), soft); err != nil || got {
		t.Errorf("a soft bounce suppressed %s (suppressed=%v, err=%v)", soft, got, err)
	}
}

// The warmup claim and finalize, over the wire, against a real participant pair.
// Warmup has its own claim with the same four states, and MarkWarmupSent's
// side effects (thread advance, daily counter) are guarded so a repeat cannot
// double-count — which is what a lost response makes reachable.
func TestRemoteWarmupClaimAndFinalizeAreIdempotent(t *testing.T) {
	ctx, f := setupWarmup(t)
	srv := fleetListener(t, f.q, itKeyring(t, f.q), f.core)
	worker := remoteOutcomeCore(t, srv.URL)

	job, err := worker.GetWarmupSendJob(ctx, f.a.String(), f.ws1.String())
	if err != nil {
		t.Fatalf("GetWarmupSendJob: %v", err)
	}
	if job.Skip {
		t.Fatalf("job = %+v, want a real warmup send", job)
	}
	outcome, err := worker.ClaimWarmupSend(ctx, job)
	if err != nil {
		t.Fatalf("ClaimWarmupSend: %v", err)
	}
	if outcome != coreapi.ClaimWon {
		t.Fatalf("outcome = %v, want ClaimWon", outcome)
	}
	// A second claim on the same row loses, exactly as it does in process: this
	// is the lost-response case for a warmup claim.
	if again, err := worker.ClaimWarmupSend(ctx, job); err != nil || again != coreapi.ClaimSkip {
		t.Fatalf("second claim = %v, %v; want ClaimSkip", again, err)
	}

	const msgID = "<warm-once@acme.test>"
	for i := range 3 {
		if err := worker.MarkWarmupSent(ctx, job, msgID); err != nil {
			t.Fatalf("MarkWarmupSent attempt %d: %v", i+1, err)
		}
	}
	var status, stored string
	if err := f.raw.QueryRow(ctx,
		`SELECT status, message_id FROM warmup_sends WHERE id = $1 AND workspace_id = $2`,
		uuid.MustParse(job.SendID), f.ws1).Scan(&status, &stored); err != nil {
		t.Fatalf("read warmup send: %v", err)
	}
	if status != "sent" || stored != msgID {
		t.Errorf("warmup send = %q/%q, want sent/%s", status, stored, msgID)
	}
	// The daily counter moved exactly once, which is what the 'sending' guard on
	// the finalize buys: three finalizes, one increment.
	var sent int
	if err := f.raw.QueryRow(ctx,
		`SELECT coalesce(sum(sent), 0) FROM warmup_daily_stats WHERE mailbox_id = $1 AND workspace_id = $2`,
		f.a, f.ws1).Scan(&sent); err != nil {
		t.Fatalf("read warmup stats: %v", err)
	}
	if sent != 1 {
		t.Errorf("warmup sent counter = %d after three finalizes, want 1", sent)
	}
}

// The webhook delivery outcomes, over the wire. All three are guarded on
// status='pending', so a repeat — which is what a lost response produces — is a
// clean no-op rather than a second attempt count.
func TestRemoteWebhookOutcomesAreGuardedAndIdempotent(t *testing.T) {
	ctx, f := setupPool(t)
	deliveryID, _ := seedWebhookDelivery(t, ctx, f)
	client, _ := faultyFleet(t, f)
	// The three webhook outcomes are deliberately NOT on coreapi.Client — the
	// worker consumes them through its own narrow webhookworker.Core, the same
	// "don't widen a 40-method interface for one call site" trade. OutcomeSource
	// is the seam they DO live on, so that is what the test asserts through.
	worker, ok := client.(OutcomeSource)
	if !ok {
		t.Fatalf("the remote-sourced client (%T) does not satisfy OutcomeSource", client)
	}

	for i := range 3 {
		if err := worker.MarkWebhookDelivered(ctx, deliveryID.String(), f.ws.String(), 1, 200); err != nil {
			t.Fatalf("MarkWebhookDelivered attempt %d: %v", i+1, err)
		}
	}
	var status string
	var attempts int32
	if err := f.pool.QueryRow(ctx,
		`SELECT status, attempts FROM webhook_deliveries WHERE id = $1 AND workspace_id = $2`,
		deliveryID, f.ws).Scan(&status, &attempts); err != nil {
		t.Fatalf("read delivery: %v", err)
	}
	if status != "delivered" || attempts != 1 {
		t.Errorf("delivery = %q/%d, want delivered/1", status, attempts)
	}
	// A 'failed' finalize on an already-delivered row touches nothing: the
	// 'pending' guard is what makes a late retry harmless.
	if err := worker.MarkWebhookFailed(ctx, deliveryID.String(), f.ws.String(), 9, "late", nil); err != nil {
		t.Fatalf("MarkWebhookFailed on a delivered row: %v", err)
	}
	if err := f.pool.QueryRow(ctx,
		`SELECT status, attempts FROM webhook_deliveries WHERE id = $1 AND workspace_id = $2`,
		deliveryID, f.ws).Scan(&status, &attempts); err != nil {
		t.Fatalf("re-read delivery: %v", err)
	}
	if status != "delivered" || attempts != 1 {
		t.Errorf("delivery = %q/%d after a late failure, want delivered/1", status, attempts)
	}
}
