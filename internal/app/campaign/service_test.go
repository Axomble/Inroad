package campaign

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

type fakeStore struct {
	status      string
	enrollments []Enrollment // enrollments returned by EnrollTx
	steps       int64        // CountSteps result
	// enrollCalled is set to true when EnrollTx runs so tests can assert the
	// tx path is actually exercised.
	enrollCalled bool
	// campaigns keyed by (workspaceID, campaignID). Used by the cross-tenant
	// test to prove Get returns ErrNotFound for a campaign in another workspace.
	campaigns map[[2]uuid.UUID]gen.Campaign
	// detail-view fixtures.
	stepList     []gen.SequenceStep
	enrollCounts map[string]int64

	// metrics fixtures. sendStats backs Stats (Sent is read from
	// sendStats["sent"]); opens/clicks/stopReasons back EngagementCounts and
	// StopReasonCounts respectively.
	sendStats   map[string]int64
	opens       int64
	clicks      int64
	stopReasons map[string]int64
	// engagementCalls/stopReasonCalls count invocations so cache tests can
	// assert a second Detail call within the TTL doesn't re-query them.
	engagementCalls int
	stopReasonCalls int

	// tracking-toggle fixtures/spies.
	setTrackingWS, setTrackingID uuid.UUID
	setTrackingEnabled           bool
	setTrackingCalls             int
	setTrackingErr               error
}

func (*fakeStore) Create(_ context.Context, _ uuid.UUID, in CreateInput) (gen.Campaign, error) {
	return gen.Campaign{ID: uuid.New(), Name: in.Name, Subject: in.Subject}, nil
}
func (f *fakeStore) Get(_ context.Context, ws, id uuid.UUID) (gen.Campaign, error) {
	if f.campaigns != nil {
		c, ok := f.campaigns[[2]uuid.UUID{ws, id}]
		if !ok {
			return gen.Campaign{}, errNotFound
		}
		return c, nil
	}
	return gen.Campaign{Status: f.status}, nil
}
func (*fakeStore) List(context.Context, uuid.UUID) ([]gen.Campaign, error) { return nil, nil }
func (f *fakeStore) Stats(context.Context, uuid.UUID, uuid.UUID) (map[string]int64, error) {
	return f.sendStats, nil
}
func (f *fakeStore) CountSteps(context.Context, uuid.UUID, uuid.UUID) (int64, error) {
	return f.steps, nil
}
func (f *fakeStore) EnrollTx(context.Context, uuid.UUID, uuid.UUID) ([]Enrollment, error) {
	f.enrollCalled = true
	return f.enrollments, nil
}
func (*fakeStore) Reschedule(context.Context, uuid.UUID, uuid.UUID, time.Time) error { return nil }
func (f *fakeStore) ListSteps(context.Context, uuid.UUID, uuid.UUID) ([]gen.SequenceStep, error) {
	return f.stepList, nil
}
func (f *fakeStore) EnrollmentCounts(context.Context, uuid.UUID, uuid.UUID) (map[string]int64, error) {
	return f.enrollCounts, nil
}
func (f *fakeStore) EngagementCounts(context.Context, uuid.UUID, uuid.UUID) (int64, int64, error) {
	f.engagementCalls++
	return f.opens, f.clicks, nil
}
func (f *fakeStore) StopReasonCounts(context.Context, uuid.UUID, uuid.UUID) (map[string]int64, error) {
	f.stopReasonCalls++
	return f.stopReasons, nil
}
func (f *fakeStore) SetTracking(_ context.Context, ws, id uuid.UUID, enabled bool) error {
	f.setTrackingCalls++
	f.setTrackingWS, f.setTrackingID, f.setTrackingEnabled = ws, id, enabled
	return f.setTrackingErr
}

// errNotFound is what the sqlc-backed Get returns when the row isn't in the
// caller's workspace (pgx.ErrNoRows). The fake stands in with a sentinel so
// tests don't have to import pgx.
var errNotFound = errors.New("no rows")

// selectiveEnqueuer succeeds on any id it hasn't been told to fail. Used to
// prove the service tallies partial-enqueue failures rather than swallowing
// them.
type selectiveEnqueuer struct {
	fail     map[string]bool
	enqueued []string
}

func (s *selectiveEnqueuer) EnqueueAdvanceAt(enrollmentID, _ string, _ time.Time) error {
	if s.fail[enrollmentID] {
		return errors.New("redis unavailable")
	}
	s.enqueued = append(s.enqueued, enrollmentID)
	return nil
}

type fakeEnqueuer struct {
	enqueued []string
	// at records the ProcessAt time each advance was scheduled with, keyed by
	// enrollment id, so a test can assert it equals the enrollment's next_due_at.
	at map[string]time.Time
}

func (f *fakeEnqueuer) EnqueueAdvanceAt(enrollmentID, _ string, t time.Time) error {
	f.enqueued = append(f.enqueued, enrollmentID)
	if f.at == nil {
		f.at = map[string]time.Time{}
	}
	f.at[enrollmentID] = t
	return nil
}

type okChecker struct{ active bool }

func (o okChecker) MailboxActive(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return o.active, nil
}
func (o okChecker) ListExists(context.Context, uuid.UUID, uuid.UUID) (bool, error) { return true, nil }

func TestCreateRejectsInactiveMailbox(t *testing.T) {
	svc := NewService(&fakeStore{}, okChecker{active: false})
	_, err := svc.Create(context.Background(), uuid.New(), CreateInput{
		Name: "Q3", Subject: "Hi", BodyText: "hello", MailboxID: uuid.New(), ListID: uuid.New(),
	})
	if err != ErrMailboxNotActive {
		t.Fatalf("expected ErrMailboxNotActive, got %v", err)
	}
}

func TestCreateSucceeds(t *testing.T) {
	svc := NewService(&fakeStore{}, okChecker{active: true})
	c, err := svc.Create(context.Background(), uuid.New(), CreateInput{
		Name: "Q3", Subject: "Hi", BodyText: "hello", MailboxID: uuid.New(), ListID: uuid.New(),
	})
	if err != nil || c.Name != "Q3" {
		t.Fatalf("Create: %v %+v", err, c)
	}
}

func TestLaunchRejectsAlreadyLaunched(t *testing.T) {
	svc := NewService(&fakeStore{status: string(StatusRunning)}, okChecker{active: true})
	_, err := svc.Launch(context.Background(), uuid.New(), uuid.New(), &fakeEnqueuer{})
	if err != ErrAlreadyLaunched {
		t.Fatalf("expected ErrAlreadyLaunched, got %v", err)
	}
}

func TestLaunchRejectsNoSteps(t *testing.T) {
	// A draft campaign with a non-empty list but zero steps can't launch.
	svc := NewService(&fakeStore{status: string(StatusDraft), steps: 0}, okChecker{active: true})
	_, err := svc.Launch(context.Background(), uuid.New(), uuid.New(), &fakeEnqueuer{})
	if err != ErrNoSteps {
		t.Fatalf("expected ErrNoSteps, got %v", err)
	}
}

func TestLaunchRejectsEmptyList(t *testing.T) {
	// Steps exist, but EnrollTx returns no enrollments (empty list).
	svc := NewService(&fakeStore{status: string(StatusDraft), steps: 1}, okChecker{active: true})
	_, err := svc.Launch(context.Background(), uuid.New(), uuid.New(), &fakeEnqueuer{})
	if err != ErrEmptyList {
		t.Fatalf("expected ErrEmptyList, got %v", err)
	}
}

func TestLaunchSucceeds(t *testing.T) {
	// Distinct next_due_at per enrollment (the staggered values EnrollListMembers
	// assigns) so the alignment assertion below is meaningful.
	base := time.Now()
	enrollments := []Enrollment{
		{ID: uuid.New(), NextDueAt: base},
		{ID: uuid.New(), NextDueAt: base.Add(2 * time.Second)},
		{ID: uuid.New(), NextDueAt: base.Add(4 * time.Second)},
	}
	store := &fakeStore{status: string(StatusDraft), steps: 2, enrollments: enrollments}
	enq := &fakeEnqueuer{}
	svc := NewService(store, okChecker{active: true})
	res, err := svc.Launch(context.Background(), uuid.New(), uuid.New(), enq)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if res.EnqueuedCount != len(enrollments) {
		t.Fatalf("queued: got %d want %d", res.EnqueuedCount, len(enrollments))
	}
	if res.TotalEnrolled != len(enrollments) {
		t.Fatalf("total enrolled: got %d want %d", res.TotalEnrolled, len(enrollments))
	}
	if res.FailedEnqueueCount != 0 {
		t.Fatalf("expected no failed enqueues, got %d", res.FailedEnqueueCount)
	}
	if len(enq.enqueued) != len(enrollments) {
		t.Fatalf("enqueued: got %d want %d", len(enq.enqueued), len(enrollments))
	}
	// Fix B: each advance is scheduled at exactly the enrollment's DB-assigned
	// next_due_at, not a Go-recomputed offset — so the scheduled task and the
	// sweeper's due cursor stay aligned.
	for _, e := range enrollments {
		if got := enq.at[e.ID.String()]; !got.Equal(e.NextDueAt) {
			t.Fatalf("enqueue ETA for %s: got %v want %v", e.ID, got, e.NextDueAt)
		}
	}
	if !store.enrollCalled {
		t.Fatal("expected EnrollTx to be called")
	}
}

// TestLaunchCountsPartialEnqueueFailures proves the service no longer
// swallows enqueue errors - a redis blip that drops individual ids must show
// up in FailedEnqueueCount, so callers can log/alert and the stuck-send
// sweeper knows there's work to reconcile.
func TestLaunchCountsPartialEnqueueFailures(t *testing.T) {
	ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	enrollments := []Enrollment{{ID: ids[0]}, {ID: ids[1]}, {ID: ids[2]}}
	store := &fakeStore{status: string(StatusDraft), steps: 1, enrollments: enrollments}
	enq := &selectiveEnqueuer{fail: map[string]bool{ids[1].String(): true}}
	svc := NewService(store, okChecker{active: true})

	res, err := svc.Launch(context.Background(), uuid.New(), uuid.New(), enq)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if res.TotalEnrolled != 3 || res.EnqueuedCount != 2 || res.FailedEnqueueCount != 1 {
		t.Fatalf("counts wrong: %+v", res)
	}
}

func TestDetailIncludesStepsAndEnrollmentCounts(t *testing.T) {
	ws, id := uuid.New(), uuid.New()
	store := &fakeStore{
		campaigns:    map[[2]uuid.UUID]gen.Campaign{{ws, id}: {ID: id, WorkspaceID: ws, Name: "Q3", Status: "running"}},
		stepList:     []gen.SequenceStep{{StepOrder: 1}, {StepOrder: 2}},
		enrollCounts: map[string]int64{"active": 5, "completed": 1},
	}
	svc := NewService(store, okChecker{active: true})
	d, err := svc.Detail(context.Background(), ws, id)
	if err != nil {
		t.Fatalf("Detail: %v", err)
	}
	if len(d.Steps) != 2 {
		t.Fatalf("want 2 steps, got %d", len(d.Steps))
	}
	if d.Enrollments["active"] != 5 || d.Enrollments["completed"] != 1 {
		t.Fatalf("enrollment counts wrong: %+v", d.Enrollments)
	}
}

func TestDetailCrossTenantIsNotFound(t *testing.T) {
	store := &fakeStore{campaigns: map[[2]uuid.UUID]gen.Campaign{
		{uuid.New(), uuid.New()}: {Name: "foreign"},
	}}
	svc := NewService(store, okChecker{active: true})
	if _, err := svc.Detail(context.Background(), uuid.New(), uuid.New()); err != ErrNotFound {
		t.Fatalf("want ErrNotFound for cross-tenant detail, got %v", err)
	}
}

// TestCrossTenantGetReturnsNotFound guards defense-in-depth on the read
// path: Get is workspace-scoped at the SQL layer (see queries/campaign.sql
// "WHERE id = $1 AND workspace_id = $2"), so a caller supplying a campaign
// id that belongs to a different tenant must see "not found", not another
// tenant's campaign row.
func TestCrossTenantGetReturnsNotFound(t *testing.T) {
	otherWS := uuid.New()
	callerWS := uuid.New()
	campaignID := uuid.New()

	store := &fakeStore{
		campaigns: map[[2]uuid.UUID]gen.Campaign{
			{otherWS, campaignID}: {ID: campaignID, WorkspaceID: otherWS, Name: "foreign"},
		},
	}
	svc := NewService(store, okChecker{active: true})

	if _, err := svc.Get(context.Background(), callerWS, campaignID); err != errNotFound {
		t.Fatalf("expected cross-tenant Get to fail with not-found, got %v", err)
	}
}

// TestDetailMetricsComputesRates proves Metrics aggregates the seeded raw
// counts and turns them into rates, including the stop_reason -> field
// mapping (unsub comes from 'suppressed', not the workspace suppression
// table -- see stopReasonSuppressed in service.go).
func TestDetailMetricsComputesRates(t *testing.T) {
	ws, id := uuid.New(), uuid.New()
	store := &fakeStore{
		campaigns:   map[[2]uuid.UUID]gen.Campaign{{ws, id}: {ID: id, WorkspaceID: ws, Status: "running"}},
		sendStats:   map[string]int64{"sent": 100, "queued": 5},
		opens:       40,
		clicks:      20,
		stopReasons: map[string]int64{"replied": 10, "bounced": 5, "suppressed": 2, "manual": 1},
	}
	svc := NewService(store, okChecker{active: true})
	d, err := svc.Detail(context.Background(), ws, id)
	if err != nil {
		t.Fatalf("Detail: %v", err)
	}
	m := d.Metrics
	if m.Sent != 100 || m.OpensIndicative != 40 || m.Clicks != 20 || m.Replies != 10 || m.Bounces != 5 || m.Unsubscribes != 2 {
		t.Fatalf("counts wrong: %+v", m)
	}
	if m.OpenRate != 0.4 || m.ClickRate != 0.2 || m.ReplyRate != 0.1 || m.BounceRate != 0.05 || m.UnsubRate != 0.02 {
		t.Fatalf("rates wrong: %+v", m)
	}
}

// TestDetailMetricsZeroSentGuardsDivideByZero covers a draft or just-launched
// campaign with no sends yet: all rates must come back 0, not NaN/Inf.
func TestDetailMetricsZeroSentGuardsDivideByZero(t *testing.T) {
	ws, id := uuid.New(), uuid.New()
	store := &fakeStore{
		campaigns:   map[[2]uuid.UUID]gen.Campaign{{ws, id}: {ID: id, WorkspaceID: ws, Status: "draft"}},
		sendStats:   map[string]int64{},
		stopReasons: map[string]int64{},
	}
	svc := NewService(store, okChecker{active: true})
	d, err := svc.Detail(context.Background(), ws, id)
	if err != nil {
		t.Fatalf("Detail: %v", err)
	}
	m := d.Metrics
	if m.Sent != 0 {
		t.Fatalf("expected Sent=0, got %d", m.Sent)
	}
	if m.OpenRate != 0 || m.ClickRate != 0 || m.ReplyRate != 0 || m.BounceRate != 0 || m.UnsubRate != 0 {
		t.Fatalf("expected all rates 0 for Sent=0, got %+v", m)
	}
}

// TestDetailMetricsCachedWithinTTL proves the metrics aggregation store calls
// (EngagementCounts, StopReasonCounts) run once per campaign per TTL window,
// not on every Detail call -- the point of metricsCache. It also proves the
// cache reshape actually fixes the cross-field staleness bug: Sent (and
// therefore every rate) is recomputed fresh on every call, even when the
// raw engagement aggregates are served from cache, so Metrics.Sent can never
// lag the response's top-level stats.sent.
func TestDetailMetricsCachedWithinTTL(t *testing.T) {
	ws, id := uuid.New(), uuid.New()
	store := &fakeStore{
		campaigns:   map[[2]uuid.UUID]gen.Campaign{{ws, id}: {ID: id, WorkspaceID: ws, Status: "running"}},
		sendStats:   map[string]int64{"sent": 10},
		opens:       5,
		clicks:      2,
		stopReasons: map[string]int64{},
	}
	svc := NewService(store, okChecker{active: true})
	if _, err := svc.Detail(context.Background(), ws, id); err != nil {
		t.Fatalf("Detail (1st): %v", err)
	}
	if _, err := svc.Detail(context.Background(), ws, id); err != nil {
		t.Fatalf("Detail (2nd): %v", err)
	}
	if store.engagementCalls != 1 || store.stopReasonCalls != 1 {
		t.Fatalf("expected metrics store calls cached (1 each), got engagement=%d stopReason=%d",
			store.engagementCalls, store.stopReasonCalls)
	}

	// Sending continues between requests (Sent grows) while the cache is
	// still warm for the heavy aggregates -- Metrics.Sent must track the new
	// value immediately, not the value from the first call.
	store.sendStats = map[string]int64{"sent": 20}
	d, err := svc.Detail(context.Background(), ws, id)
	if err != nil {
		t.Fatalf("Detail (3rd, post-growth): %v", err)
	}
	if d.Metrics.Sent != 20 {
		t.Fatalf("expected Metrics.Sent to track the fresh Stats() value (20), got %d (cache staleness bug)", d.Metrics.Sent)
	}
	if got := d.Metrics.OpenRate; got != 0.25 { // 5 opens / 20 sent, using the still-cached opens count
		t.Fatalf("expected OpenRate recomputed against the new Sent (0.25), got %v", got)
	}
	if store.engagementCalls != 1 || store.stopReasonCalls != 1 {
		t.Fatalf("expected heavy aggregate queries still cached after Sent changed, got engagement=%d stopReason=%d",
			store.engagementCalls, store.stopReasonCalls)
	}
}

// TestDetailMetricsRecomputesAfterTTL proves the cache actually expires
// rather than serving stale metrics forever.
func TestDetailMetricsRecomputesAfterTTL(t *testing.T) {
	ws, id := uuid.New(), uuid.New()
	store := &fakeStore{
		campaigns: map[[2]uuid.UUID]gen.Campaign{{ws, id}: {ID: id, WorkspaceID: ws, Status: "running"}},
		sendStats: map[string]int64{"sent": 10},
	}
	svc := NewService(store, okChecker{active: true})

	now := time.Now()
	svc.metrics.now = func() time.Time { return now }

	if _, err := svc.Detail(context.Background(), ws, id); err != nil {
		t.Fatalf("Detail (1st): %v", err)
	}
	now = now.Add(metricsCacheTTL + time.Second) // advance past the TTL
	if _, err := svc.Detail(context.Background(), ws, id); err != nil {
		t.Fatalf("Detail (2nd): %v", err)
	}
	if store.engagementCalls != 2 || store.stopReasonCalls != 2 {
		t.Fatalf("expected recompute after TTL expiry (2 each), got engagement=%d stopReason=%d",
			store.engagementCalls, store.stopReasonCalls)
	}
}

// TestSetTrackingUpdatesFlag proves SetTracking is workspace-scoped: it
// resolves the campaign via Get first (so a cross-tenant id 404s before the
// store's update call ever runs) and forwards the requested enabled value
// verbatim.
func TestSetTrackingUpdatesFlag(t *testing.T) {
	ws, id := uuid.New(), uuid.New()
	store := &fakeStore{
		campaigns: map[[2]uuid.UUID]gen.Campaign{{ws, id}: {ID: id, WorkspaceID: ws, Status: "running"}},
	}
	svc := NewService(store, okChecker{active: true})

	if err := svc.SetTracking(context.Background(), ws, id, false); err != nil {
		t.Fatalf("SetTracking: %v", err)
	}
	if store.setTrackingCalls != 1 || store.setTrackingWS != ws || store.setTrackingID != id || store.setTrackingEnabled != false {
		t.Fatalf("SetTracking store call wrong: calls=%d ws=%v id=%v enabled=%v",
			store.setTrackingCalls, store.setTrackingWS, store.setTrackingID, store.setTrackingEnabled)
	}
}

// TestSetTrackingCrossTenantIsNotFound proves a campaign id from another
// workspace 404s rather than silently flipping (or no-op'ing on) another
// tenant's row.
func TestSetTrackingCrossTenantIsNotFound(t *testing.T) {
	store := &fakeStore{campaigns: map[[2]uuid.UUID]gen.Campaign{
		{uuid.New(), uuid.New()}: {Name: "foreign"},
	}}
	svc := NewService(store, okChecker{active: true})
	if err := svc.SetTracking(context.Background(), uuid.New(), uuid.New(), true); err != ErrNotFound {
		t.Fatalf("want ErrNotFound for cross-tenant SetTracking, got %v", err)
	}
	if store.setTrackingCalls != 0 {
		t.Fatalf("expected store.SetTracking not called on cross-tenant id, got %d calls", store.setTrackingCalls)
	}
}
