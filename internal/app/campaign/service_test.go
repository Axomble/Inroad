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

	// enrollment-listing fixtures/spies: enrollmentRows is returned verbatim;
	// the limit/offset the service passed (post-clamp) are recorded so tests can
	// assert the default/clamp behaviour.
	enrollmentRows                              []gen.ListCampaignEnrollmentsRow
	listEnrollmentsCalls                        int
	listEnrollmentsLimit, listEnrollmentsOffset int32

	// schedule fixtures/spies. windows is what ListWindows returns (nil means the
	// campaign has none, which resolves to the Mon–Fri default downstream);
	// windowsErr forces the load to fail. rescheduled
	// captures the cadence-computed due times Launch stamps, and replacedSchedule
	// the last schedule written.
	windows          []SendWindow
	windowsErr       error
	campaignTimezone string
	// sender-pool fixtures/spies. senders is what ListSenders returns — nil means
	// the campaign has no pool rows, which must resolve to fallbackSender rather
	// than an empty pool. replacedSenders/replacedRotationMode capture the last
	// replace so a test can prove a rejected pool was never written.
	senders              []Sender
	sendersErr           error
	fallbackSender       Sender
	fallbackSenderErr    error
	rotationMode         string
	replacedSenders      []SenderInput
	replacedRotationMode string
	replaceSendersCalls  int
	replaceSendersErr    error

	rescheduled        map[uuid.UUID]time.Time
	rescheduleErr      error
	replacedSchedule   *Plan
	replaceScheduleErr error

	// lifecycle spies. Pause/Resume/Rename/DeleteDraft themselves are exercised
	// in the black-box lifecycle_test.go; these methods exist here only so this
	// white-box fakeStore keeps satisfying the Store interface for the rest of
	// this package's tests.
	setStatusCalls   int
	renameCalls      int
	deleteDraftCalls int

	// preflight/test-send fixtures. See CountUnsuppressedAudience/
	// FirstListContact below — full coverage lives in preflight_test.go/
	// testsend_test.go's own black-box fakes.
	audienceCount       int64
	audienceCountErr    error
	firstContactName    string
	firstContactCompany string
	firstContactFound   bool
	firstContactErr     error
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
	return gen.Campaign{Status: f.status, Timezone: f.campaignTimezone, RotationMode: f.rotationMode}, nil
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

func (f *fakeStore) RescheduleBatch(_ context.Context, _ uuid.UUID, due map[uuid.UUID]time.Time) error {
	if f.rescheduleErr != nil {
		return f.rescheduleErr
	}
	f.rescheduled = due
	return nil
}

func (f *fakeStore) ListWindows(context.Context, uuid.UUID, uuid.UUID) ([]SendWindow, error) {
	if f.windowsErr != nil {
		return nil, f.windowsErr
	}
	// nil is returned as-is: an unconfigured campaign resolving to the default
	// schedule is the production behavior (cadence.ScheduleFrom), so the fake must
	// not pre-substitute it.
	return f.windows, nil
}

func (f *fakeStore) ReplaceSchedule(_ context.Context, _, _ uuid.UUID, plan Plan) error {
	if f.replaceScheduleErr != nil {
		return f.replaceScheduleErr
	}
	f.replacedSchedule = &plan
	return nil
}
func (f *fakeStore) ListSenders(context.Context, uuid.UUID, uuid.UUID) ([]Sender, error) {
	// nil is returned as-is: a campaign with no pool rows falling back to
	// campaigns.mailbox_id is the production behaviour, so the fake must not
	// pre-substitute it.
	return f.senders, f.sendersErr
}

func (f *fakeStore) FallbackSender(context.Context, uuid.UUID, uuid.UUID) (Sender, error) {
	return f.fallbackSender, f.fallbackSenderErr
}

func (f *fakeStore) ReplaceSenders(_ context.Context, ws, campaignID uuid.UUID, mode string, senders []SenderInput) error {
	f.replaceSendersCalls++
	if f.replaceSendersErr != nil {
		return f.replaceSendersErr
	}
	f.replacedRotationMode, f.replacedSenders = mode, senders
	// The read-back SetSenders does must see what was written.
	f.rotationMode = mode
	if c, ok := f.campaigns[[2]uuid.UUID{ws, campaignID}]; ok {
		c.RotationMode = mode
		f.campaigns[[2]uuid.UUID{ws, campaignID}] = c
	}
	f.senders = make([]Sender, len(senders))
	for i, s := range senders {
		f.senders[i] = Sender{MailboxID: s.MailboxID, Weight: s.Weight, Enabled: s.Enabled}
	}
	return nil
}

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
func (f *fakeStore) ListEnrollments(_ context.Context, _, _ uuid.UUID, limit, offset int32) ([]gen.ListCampaignEnrollmentsRow, error) {
	f.listEnrollmentsCalls++
	f.listEnrollmentsLimit, f.listEnrollmentsOffset = limit, offset
	return f.enrollmentRows, nil
}

func (f *fakeStore) SetStatus(_ context.Context, ws, id uuid.UUID, status CampaignStatus) error {
	f.setStatusCalls++
	f.status = string(status)
	if f.campaigns != nil {
		if c, ok := f.campaigns[[2]uuid.UUID{ws, id}]; ok {
			c.Status = string(status)
			f.campaigns[[2]uuid.UUID{ws, id}] = c
		}
	}
	return nil
}

func (f *fakeStore) Rename(_ context.Context, ws, id uuid.UUID, name string) (gen.Campaign, error) {
	f.renameCalls++
	if f.campaigns != nil {
		c, ok := f.campaigns[[2]uuid.UUID{ws, id}]
		if !ok {
			return gen.Campaign{}, errNotFound
		}
		c.Name = name
		f.campaigns[[2]uuid.UUID{ws, id}] = c
		return c, nil
	}
	return gen.Campaign{ID: id, Name: name}, nil
}

func (f *fakeStore) DeleteDraft(_ context.Context, ws, id uuid.UUID) error {
	f.deleteDraftCalls++
	if f.campaigns != nil {
		delete(f.campaigns, [2]uuid.UUID{ws, id})
	}
	return nil
}

// audienceCount/firstContact* back CountUnsuppressedAudience/FirstListContact.
// Preflight/test-send are exercised in their own black-box test files
// (preflight_test.go, testsend_test.go); these exist here only so this
// white-box fakeStore keeps satisfying the Store interface for the rest of
// this package's tests.
func (f *fakeStore) CountUnsuppressedAudience(context.Context, uuid.UUID, uuid.UUID) (int64, error) {
	return f.audienceCount, f.audienceCountErr
}

func (f *fakeStore) FirstListContact(context.Context, uuid.UUID, uuid.UUID) (string, string, bool, error) {
	return f.firstContactName, f.firstContactCompany, f.firstContactFound, f.firstContactErr
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
	if !errors.Is(err, ErrMailboxNotActive) {
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
	if !errors.Is(err, ErrAlreadyLaunched) {
		t.Fatalf("expected ErrAlreadyLaunched, got %v", err)
	}
}

func TestLaunchRejectsNoSteps(t *testing.T) {
	// A draft campaign with a non-empty list but zero steps can't launch.
	svc := NewService(&fakeStore{status: string(StatusDraft), steps: 0}, okChecker{active: true})
	_, err := svc.Launch(context.Background(), uuid.New(), uuid.New(), &fakeEnqueuer{})
	if !errors.Is(err, ErrNoSteps) {
		t.Fatalf("expected ErrNoSteps, got %v", err)
	}
}

func TestLaunchRejectsEmptyList(t *testing.T) {
	// Steps exist, but EnrollTx returns no enrollments (empty list).
	svc := NewService(&fakeStore{status: string(StatusDraft), steps: 1}, okChecker{active: true})
	_, err := svc.Launch(context.Background(), uuid.New(), uuid.New(), &fakeEnqueuer{})
	if !errors.Is(err, ErrEmptyList) {
		t.Fatalf("expected ErrEmptyList, got %v", err)
	}
}

func TestLaunchSucceeds(t *testing.T) {
	// NextDueAt here is the placeholder EnrollListMembers now returns; Launch
	// overwrites it with a cadence instant. Distinct values make it visible if the
	// launcher ever went back to enqueuing against the insert's value instead of
	// the one it stamped.
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
	// Each advance is scheduled at exactly the cadence instant stamped on its
	// enrollment, so the scheduled task and the sweeper's due cursor stay aligned.
	// The stamped value — not the placeholder next_due_at the insert returned — is
	// the contract now that Go, rather than the INSERT, decides the instant.
	if len(store.rescheduled) != len(enrollments) {
		t.Fatalf("stamped due times: got %d want %d", len(store.rescheduled), len(enrollments))
	}
	for _, e := range enrollments {
		stamped, ok := store.rescheduled[e.ID]
		if !ok {
			t.Fatalf("enrollment %s got no cadence due time", e.ID)
		}
		if got := enq.at[e.ID.String()]; !got.Equal(stamped) {
			t.Fatalf("enqueue ETA for %s: got %v want the stamped %v", e.ID, got, stamped)
		}
		if stamped.Second() == 0 && stamped.Nanosecond() == 0 {
			t.Errorf("enrollment %s scheduled at %v, exactly on a clock boundary", e.ID, stamped)
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
	if _, err := svc.Detail(context.Background(), uuid.New(), uuid.New()); !errors.Is(err, ErrNotFound) {
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

	if _, err := svc.Get(context.Background(), callerWS, campaignID); !errors.Is(err, errNotFound) {
		t.Fatalf("expected cross-tenant Get to fail with not-found, got %v", err)
	}
}

// TestDetailMetricsComputesRates proves Metrics aggregates the seeded raw
// counts and turns them into rates using their respective denominators:
// OpenRate/ClickRate against Sent (per-send), ReplyRate/BounceRate/UnsubRate
// against the enrolled-contact total (per-contact, sum of Enrollments) --
// including the stop_reason -> field mapping (unsub comes from 'suppressed',
// not the workspace suppression table -- see stopReasonSuppressed in
// service.go). Sent (200) and enrolled (50) are deliberately different so a
// regression back to a single shared denominator would be caught.
func TestDetailMetricsComputesRates(t *testing.T) {
	ws, id := uuid.New(), uuid.New()
	store := &fakeStore{
		campaigns:    map[[2]uuid.UUID]gen.Campaign{{ws, id}: {ID: id, WorkspaceID: ws, Status: "running"}},
		sendStats:    map[string]int64{"sent": 200, "queued": 5},
		opens:        40,
		clicks:       20,
		stopReasons:  map[string]int64{"replied": 10, "bounced": 5, "suppressed": 2, "manual": 1},
		enrollCounts: map[string]int64{"active": 30, "completed": 15, "stopped": 5}, // sums to 50
	}
	svc := NewService(store, okChecker{active: true})
	d, err := svc.Detail(context.Background(), ws, id)
	if err != nil {
		t.Fatalf("Detail: %v", err)
	}
	m := d.Metrics
	if m.Sent != 200 || m.OpensIndicative != 40 || m.Clicks != 20 || m.Replies != 10 || m.Bounces != 5 || m.Unsubscribes != 2 {
		t.Fatalf("counts wrong: %+v", m)
	}
	if m.OpenRate != 0.2 || m.ClickRate != 0.1 {
		t.Fatalf("per-send rates wrong: OpenRate=%v ClickRate=%v", m.OpenRate, m.ClickRate)
	}
	if m.ReplyRate != 0.2 || m.BounceRate != 0.1 || m.UnsubRate != 0.04 {
		t.Fatalf("per-contact rates wrong: ReplyRate=%v BounceRate=%v UnsubRate=%v", m.ReplyRate, m.BounceRate, m.UnsubRate)
	}
}

// TestDetailMetricsReplyRateUsesEnrolledDenominator is the direct regression
// guard for the multi-step undercounting bug: a 3-step campaign sends 3x per
// contact, so dividing per-contact reply/bounce/unsub counts by the per-send
// Sent total reads ~3x low. sent=300 (100 contacts x 3 steps), enrolled=100,
// replies=100 (every contact replied) must yield ReplyRate==1.0, not 0.33.
// OpenRate/ClickRate stay on the Sent denominator (per-send is correct there).
func TestDetailMetricsReplyRateUsesEnrolledDenominator(t *testing.T) {
	ws, id := uuid.New(), uuid.New()
	store := &fakeStore{
		campaigns:    map[[2]uuid.UUID]gen.Campaign{{ws, id}: {ID: id, WorkspaceID: ws, Status: "running"}},
		sendStats:    map[string]int64{"sent": 300},
		opens:        30,
		clicks:       15,
		stopReasons:  map[string]int64{"replied": 100},
		enrollCounts: map[string]int64{"completed": 100},
	}
	svc := NewService(store, okChecker{active: true})
	d, err := svc.Detail(context.Background(), ws, id)
	if err != nil {
		t.Fatalf("Detail: %v", err)
	}
	if got := d.Metrics.ReplyRate; got != 1.0 {
		t.Fatalf("expected ReplyRate=1.0 (100 replies / 100 enrolled), got %v -- looks like it divided by Sent instead", got)
	}
	if got := d.Metrics.OpenRate; got != 0.1 { // 30 opens / 300 sent -- still per-send, unaffected by the fix
		t.Fatalf("expected OpenRate=0.1 (per-send, unaffected), got %v", got)
	}
}

// TestDetailMetricsZeroSentGuardsDivideByZero covers a draft or just-launched
// campaign with no sends/enrollments yet: all rates must come back 0, not
// NaN/Inf, regardless of which denominator (Sent or totalEnrolled) is zero.
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
		t.Fatalf("expected all rates 0 for Sent=0/enrolled=0, got %+v", m)
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
	if err := svc.SetTracking(context.Background(), uuid.New(), uuid.New(), true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound for cross-tenant SetTracking, got %v", err)
	}
	if store.setTrackingCalls != 0 {
		t.Fatalf("expected store.SetTracking not called on cross-tenant id, got %d calls", store.setTrackingCalls)
	}
}

// TestListEnrollmentsCrossTenantIsNotFound proves the ownership check runs
// before any enrollment read: a campaign id from another workspace 404s and the
// store's ListEnrollments is never called (no cross-tenant contact leak).
func TestListEnrollmentsCrossTenantIsNotFound(t *testing.T) {
	store := &fakeStore{campaigns: map[[2]uuid.UUID]gen.Campaign{
		{uuid.New(), uuid.New()}: {Name: "foreign"},
	}}
	svc := NewService(store, okChecker{active: true})
	if _, err := svc.ListEnrollments(context.Background(), uuid.New(), uuid.New(), 100, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound for cross-tenant ListEnrollments, got %v", err)
	}
	if store.listEnrollmentsCalls != 0 {
		t.Fatalf("expected store.ListEnrollments not called on cross-tenant id, got %d calls", store.listEnrollmentsCalls)
	}
}

// TestListEnrollmentsClampsPagination proves limit is defaulted/clamped to
// [1,500] and a negative offset floored to 0 before hitting the store.
func TestListEnrollmentsClampsPagination(t *testing.T) {
	ws, id := uuid.New(), uuid.New()
	newSvc := func() (*Service, *fakeStore) {
		store := &fakeStore{campaigns: map[[2]uuid.UUID]gen.Campaign{{ws, id}: {ID: id, WorkspaceID: ws}}}
		return NewService(store, okChecker{active: true}), store
	}
	cases := []struct {
		name                  string
		inLimit, inOffset     int32
		wantLimit, wantOffset int32
	}{
		{"zero limit defaults to 100", 0, 0, defaultEnrollmentLimit, 0},
		{"negative limit defaults to 100", -5, 0, defaultEnrollmentLimit, 0},
		{"over-max limit clamps to 500", 10000, 0, maxEnrollmentLimit, 0},
		{"in-range limit preserved", 250, 40, 250, 40},
		{"negative offset floored to 0", 50, -3, 50, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, store := newSvc()
			if _, err := svc.ListEnrollments(context.Background(), ws, id, tc.inLimit, tc.inOffset); err != nil {
				t.Fatalf("ListEnrollments: %v", err)
			}
			if store.listEnrollmentsLimit != tc.wantLimit || store.listEnrollmentsOffset != tc.wantOffset {
				t.Fatalf("clamp wrong: got limit=%d offset=%d want limit=%d offset=%d",
					store.listEnrollmentsLimit, store.listEnrollmentsOffset, tc.wantLimit, tc.wantOffset)
			}
		})
	}
}
