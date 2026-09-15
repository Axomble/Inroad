package fleet

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// sessionVerifier stands in an authenticated human with the given workspace role.
type sessionVerifier struct {
	ws   uuid.UUID
	role string
}

func (v sessionVerifier) Verify(_ context.Context, _ *http.Request) (auth.Principal, bool, error) {
	return auth.Principal{
		Kind:        auth.KindSession,
		UserID:      uuid.NewString(),
		WorkspaceID: v.ws.String(),
		Role:        v.role,
	}, true, nil
}

func mountedRouter(h *Handler, v auth.Verifier) http.Handler {
	r := chi.NewRouter()
	r.Use(auth.RequireAuth(v))
	r.Mount("/fleet", h.Routes())
	return r
}

// call drives one GET through the real mounted router as an admin session.
func call(t *testing.T, store Store, path string) *httptest.ResponseRecorder {
	t.Helper()
	ws := uuid.New()
	h := NewHandler(NewService(store, WithClock(func() time.Time { return frozen })))
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/fleet"+path, http.NoBody)
	w := httptest.NewRecorder()
	mountedRouter(h, sessionVerifier{ws: ws, role: "admin"}).ServeHTTP(w, r)
	return w
}

func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body)
	}
	var out T
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s: %v", w.Body, err)
	}
	return out
}

// The worker payload is what an operator reads a degrading worker off, so the
// pairs that make that possible without arithmetic — attempts with successes,
// auth failures with throttles — are asserted field by field rather than by a
// length check.
func TestWorkerResponseCarriesEachCountSeparately(t *testing.T) {
	store := &fakeStore{
		workers: []gen.ListWorkspaceFleetWorkersRow{workerRow("w-1", frozen.Add(-time.Minute))},
		signals: []gen.RollupWorkspaceFleetProviderSignalsRow{{
			WorkerID: "w-1", Provider: "gmail", Operation: "send",
			Attempts: 400, Successes: 310, AuthFailures: 42, Throttled: 30, Blocked: 9, Rejected: 6,
		}},
	}

	body := decode[workerListResponse](t, call(t, store, "/workers"))
	if len(body.Workers) != 1 {
		t.Fatalf("got %d workers, want 1", len(body.Workers))
	}
	got := body.Workers[0]

	if got.WorkerID != "w-1" || got.EgressIP != "203.0.113.7" || got.IDFamily != "ipv4" {
		t.Errorf("identity = %+v, want w-1 / 203.0.113.7 / ipv4", got)
	}
	if !got.Live {
		t.Error("a worker seen a minute ago is reported dead")
	}
	if got.MailboxCount != 3 || got.DegradedMailboxCount != 1 {
		t.Errorf("mailbox counts = %d/%d, want 3/1", got.MailboxCount, got.DegradedMailboxCount)
	}
	if got.LastSeenAt != frozen.Add(-time.Minute).Format(time.RFC3339) {
		t.Errorf("last_seen_at = %q, want RFC3339 %q", got.LastSeenAt, frozen.Add(-time.Minute).Format(time.RFC3339))
	}

	if len(got.Signals) != 1 {
		t.Fatalf("got %d signal rows, want 1", len(got.Signals))
	}
	want := providerSignalsResponse{
		Provider: "gmail", Operation: "send",
		Attempts: 400, Successes: 310, AuthFailures: 42, Throttled: 30, Blocked: 9, Rejected: 6,
	}
	if got.Signals[0] != want {
		t.Errorf("signals = %+v, want %+v — every count travels separately so the client can pair "+
			"them; summing any two here would make a degrading worker unreadable", got.Signals[0], want)
	}
}

// Auth failures are the provider-side signal the whole table was collected for.
// They must reach the wire under their own name, not folded into a total — a
// regression that summed them into `throttled` would still pass a test that only
// checked the row rendered.
func TestAuthFailuresAreTheirOwnWireField(t *testing.T) {
	store := &fakeStore{
		workers: []gen.ListWorkspaceFleetWorkersRow{workerRow("w-1", frozen)},
		signals: []gen.RollupWorkspaceFleetProviderSignalsRow{
			{WorkerID: "w-1", Provider: "smtp", Operation: "send", Attempts: 10, AuthFailures: 10},
		},
	}
	raw := call(t, store, "/workers").Body.String()
	if !strings.Contains(raw, `"auth_failures":10`) {
		t.Errorf("auth_failures is not a distinct field in %s", raw)
	}
	if !strings.Contains(raw, `"throttled":0`) {
		t.Errorf("auth failures appear to have been merged into another bucket: %s", raw)
	}
}

// The echoed window is what a client labels its column with. It must be the
// EFFECTIVE window after clamping, not the one that was asked for.
func TestTheResponseEchoesTheEffectiveWindowNotTheRequestedOne(t *testing.T) {
	store := &fakeStore{workers: []gen.ListWorkspaceFleetWorkersRow{workerRow("w-1", frozen)}}

	if got := decode[workerListResponse](t, call(t, store, "/workers")).WindowHours; got != 24 {
		t.Errorf("window_hours with no parameter = %d, want 24", got)
	}
	if got := decode[workerListResponse](t, call(t, store, "/workers?window_hours=6")).WindowHours; got != 6 {
		t.Errorf("window_hours=6 echoed as %d", got)
	}
	// 9000 hours is past the 30-day retention; the response must say what was
	// actually measured rather than repeat the request.
	if got := decode[workerListResponse](t, call(t, store, "/workers?window_hours=9000")).WindowHours; got != 720 {
		t.Errorf("window_hours=9000 echoed as %d, want the 720-hour cap", got)
	}
	if got := decode[workerListResponse](t, call(t, store, "/workers?window_hours=banana")).WindowHours; got != 24 {
		t.Errorf("an unparseable window echoed as %d, want the 24-hour default rather than an error", got)
	}
}

// Empty collections must serialise as [] rather than null, at every level: a
// client mapping over the fleet, or over one worker's signals, must not have to
// guard.
func TestEmptyCollectionsSerialiseAsArrays(t *testing.T) {
	raw := call(t, &fakeStore{}, "/workers").Body.String()
	if !strings.Contains(raw, `"workers":[]`) {
		t.Errorf("an empty fleet serialised as %s, want workers:[]", raw)
	}

	quiet := &fakeStore{workers: []gen.ListWorkspaceFleetWorkersRow{workerRow("w-1", frozen)}}
	if raw := call(t, quiet, "/workers").Body.String(); !strings.Contains(raw, `"signals":[]`) {
		t.Errorf("a worker with no signals serialised as %s, want signals:[]", raw)
	}

	if raw := call(t, &fakeStore{}, "/mailboxes/"+uuid.NewString()+"/decisions").Body.String(); !strings.Contains(raw, `"decisions":[]`) {
		t.Errorf("an empty decision log serialised as %s, want decisions:[]", raw)
	}
	if raw := call(t, &fakeStore{}, "/jobs").Body.String(); !strings.Contains(raw, `"jobs":[]`) {
		t.Errorf("an empty job ledger serialised as %s, want jobs:[]", raw)
	}
}

// The decision's prose is rendered EXACTLY as recorded. It is built only through
// fleetdecision's constructors so that a forced decision never claims a score
// comparison nobody made; any reshaping here would reintroduce the claim the
// type was designed to prevent.
func TestDecisionReasonIsServedVerbatimAndNeitherIDIsEchoed(t *testing.T) {
	reason := "chose w-1 (score 0.82) over w-2 (score 0.31), 4 candidates considered"
	worker := "w-1"
	store := &fakeStore{decision: []gen.FleetDecision{{
		ID:          uuid.New(),
		Kind:        "assign",
		WorkerID:    &worker,
		Reason:      reason,
		TriggeredBy: "auto:assign",
		CreatedAt:   ts(frozen),
	}}}

	w := call(t, store, "/mailboxes/"+uuid.NewString()+"/decisions")
	body := decode[decisionListResponse](t, w)
	if len(body.Decisions) != 1 {
		t.Fatalf("got %d decisions, want 1", len(body.Decisions))
	}
	if body.Decisions[0].Reason != reason {
		t.Errorf("reason = %q, want it verbatim: %q", body.Decisions[0].Reason, reason)
	}
	if body.Decisions[0].WorkerID == nil || *body.Decisions[0].WorkerID != worker {
		t.Errorf("worker_id = %v, want %q", body.Decisions[0].WorkerID, worker)
	}

	raw := w.Body.String()
	if strings.Contains(raw, "workspace_id") {
		t.Errorf("the response echoes workspace_id, which invites a client to start sending one: %s", raw)
	}
	if strings.Contains(raw, "mailbox_id") {
		t.Errorf("the response echoes mailbox_id, which the caller already named in the path: %s", raw)
	}
}

// A refusal names no destination worker, and null is the honest rendering of
// that — an empty string would sort and group as if it were a worker id.
func TestARefusalRendersWorkerIDAsNull(t *testing.T) {
	store := &fakeStore{decision: []gen.FleetDecision{{
		ID:          uuid.New(),
		Kind:        "refused",
		WorkerID:    nil,
		Reason:      "forced: no worker in the healthy band had capacity",
		TriggeredBy: "auto:refused",
		CreatedAt:   ts(frozen),
	}}}

	w := call(t, store, "/mailboxes/"+uuid.NewString()+"/decisions")
	if raw := w.Body.String(); !strings.Contains(raw, `"worker_id":null`) {
		t.Errorf("a refusal rendered worker_id as something other than null: %s", raw)
	}
}

// A malformed path id is a 400 here rather than the 404 the dead-letter route
// uses, because this endpoint has no not-found state for a 404 to hide behind —
// an unknown mailbox legitimately returns an empty list.
func TestAMalformedMailboxIDIsRejected(t *testing.T) {
	w := call(t, &fakeStore{}, "/mailboxes/not-a-uuid/decisions")
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400: %s", w.Code, w.Body)
	}
}

// THE FIELD THIS RESPONSE MUST NOT HAVE.
//
// scheduled_job_runs.error_message holds err.Error() from six handlers the
// ledger does not own, the table's own migration says it is not guaranteed to be
// free of tenant content, and the table carries no workspace_id to scope a read
// by. Serving it on a workspace-scoped endpoint would let one tenant's admin
// read an error produced while sweeping another tenant's mailbox.
func TestScheduledJobResponseWithholdsTheErrorTextButReportsTheFailure(t *testing.T) {
	failedAt := frozen.Add(-2 * time.Hour)
	store := &fakeStore{jobs: []gen.ListScheduledJobHealthRow{{
		JobName:          "domain auth sweep",
		LastStartedAt:    ts(frozen.Add(-5 * time.Minute)),
		LastFinishedAt:   ts(frozen.Add(-4 * time.Minute)),
		LastDurationMs:   61_000,
		LastOutcome:      "error",
		RunsInWindow:     288,
		FailuresInWindow: 12,
		LastFailureAt:    ts(failedAt),
	}}}

	w := call(t, store, "/jobs")
	raw := w.Body.String()
	for _, banned := range []string{"error_message", "last_error"} {
		if strings.Contains(raw, banned) {
			t.Errorf("the response carries %q; that column may hold another tenant's data and the "+
				"table has no workspace to scope it by: %s", banned, raw)
		}
	}

	body := decode[scheduledJobListResponse](t, w)
	if len(body.Jobs) != 1 {
		t.Fatalf("got %d jobs, want 1", len(body.Jobs))
	}
	job := body.Jobs[0]
	// Withholding the text must not mean withholding the failure.
	if job.LastOutcome != "error" {
		t.Errorf("last_outcome = %q, want %q", job.LastOutcome, "error")
	}
	if job.RunsInWindow != 288 || job.FailuresInWindow != 12 {
		t.Errorf("runs/failures = %d/%d, want 288/12 — the pair is what makes a failure rate readable",
			job.RunsInWindow, job.FailuresInWindow)
	}
	if job.LastFailureAt == nil || *job.LastFailureAt != failedAt.Format(time.RFC3339) {
		t.Errorf("last_failure_at = %v, want %q", job.LastFailureAt, failedAt.Format(time.RFC3339))
	}
	if job.LastDurationMs != 61_000 {
		t.Errorf("last_duration_ms = %d, want 61000", job.LastDurationMs)
	}
}

// A job that has never failed inside the window reports null rather than a zero
// time, which would render as the epoch and read as "failed in 1970".
func TestAJobThatNeverFailedReportsNullRatherThanTheZeroTime(t *testing.T) {
	store := &fakeStore{jobs: []gen.ListScheduledJobHealthRow{{
		JobName:       "warmup sweep",
		LastStartedAt: ts(frozen),
		LastOutcome:   "ok",
		RunsInWindow:  288,
	}}}
	if raw := call(t, store, "/jobs").Body.String(); !strings.Contains(raw, `"last_failure_at":null`) {
		t.Errorf("a never-failing job rendered last_failure_at as something other than null: %s", raw)
	}
}
