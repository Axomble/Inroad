package campaign

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/events"

	"github.com/inroad/inroad/internal/platform/cadence"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// Sentinel errors the handler layer maps to HTTP status codes.
var (
	ErrNotFound         = errors.New("campaign not found")
	ErrMailboxNotActive = errors.New("mailbox not found or not active")
	ErrListMissing      = errors.New("list not found")
	ErrValidation       = errors.New("invalid campaign input")
	ErrAlreadyLaunched  = errors.New("campaign already launched")
	ErrEmptyList        = errors.New("target list is empty")
	ErrNoSteps          = errors.New("campaign has no sequence steps")
	// ErrResultsUnavailable is a results read with no ResultsStore wired -- a
	// deployment/wiring fault, never a statement about the campaign.
	ErrResultsUnavailable = errors.New("campaign results are unavailable")
)

// Enqueuer schedules a sequence:advance task at a given time. Satisfied by
// *queue.Client; defined here so the domain doesn't depend on platform/queue.
// workspaceID travels alongside enrollmentID so the worker can pin workspace_id
// in its DB WHERE clauses (defense in depth on top of the UUID enrollmentID).
type Enqueuer interface {
	EnqueueAdvanceAt(enrollmentID, workspaceID string, t time.Time) error
}

// Service implements campaign use cases. It depends on the Store and
// Checker interfaces, not on the sqlc-backed struct or other domains'
// concrete stores -- dependency inversion.
//
// domainAuth/testSendEnq/limiter/suppression back the preflight report's
// domain_auth check and test-send respectively. They are all OPTIONAL
// (nil-safe: see Service.readDomainAuth, TestSend's own nil check,
// checkTestSendRateLimit, and checkRecipientNotSuppressed) and injected via
// ServiceOption rather than added as NewService parameters, so every existing
// caller of NewService(store, checker) -- and every existing unit test --
// keeps compiling unchanged.
//
// Neither TestSend nor anything else in this package ever decrypts a mailbox
// credential or dials a provider (docs/security.md invariant 1): test-send's
// actual render+send is a testsend:send task, executed by
// internal/worker/testsend in the execution plane. testSendEnq only enqueues
// that task.
type Service struct {
	store        Store
	checker      Checker
	metrics      *metricsCache
	domainAuth   DomainAuthReader
	testSendEnq  TestSendEnqueuer
	limiter      RateLimiter
	suppression  SuppressionChecker
	customFields CustomFieldReader
	results      ResultsStore
	// events announces state changes to a workspace's open tabs. NIL IS VALID
	// and means realtime is disabled — events.Emit treats it as a no-op — so a
	// service built without one behaves exactly as it did before sockets.
	events events.Publisher
}

// ServiceOption configures an optional Service dependency. See the Service
// doc comment for why these are options rather than NewService parameters.
type ServiceOption func(*Service)

// WithDomainAuth wires the preflight domain_auth check's evidence source.
// Without it, every sender domain reports as "not checked" (see
// Service.readDomainAuth) rather than the loader failing.
func WithDomainAuth(r DomainAuthReader) ServiceOption { return func(s *Service) { s.domainAuth = r } }

// WithCustomFields wires the preflight personalization_tokens check's source of
// known {{custom.*}} keys.
//
// This one is NOT nil-safe by design, and is the exception to the "optional,
// degrades quietly" rule the other options follow. Every other missing
// dependency degrades toward permissiveness (a domain reports "not checked", a
// test-send skips its rate limit); an absent key set would degrade toward
// FALSE FAILURES -- every {{custom.*}} token in the workspace would read as
// unknown and block the launch, blaming templates that are fine. So an unwired
// reader fails the preflight request outright (Service.readCustomFieldKeys),
// which is loud, obviously a wiring bug, and cannot be mistaken for a verdict
// about the campaign.
func WithCustomFields(r CustomFieldReader) ServiceOption {
	return func(s *Service) { s.customFields = r }
}

// WithResults wires the per-step / per-variant results aggregates.
//
// Like WithCustomFields and unlike the rest, an unwired reader is an ERROR
// rather than a quiet degradation: an empty report is indistinguishable from a
// campaign that has genuinely sent nothing, and a reporting screen that shows
// zeros for a campaign with thousands of sends is worse than one that says it
// could not load.
func WithResults(r ResultsStore) ServiceOption { return func(s *Service) { s.results = r } }

// WithTestSendEnqueuer wires test-send's testsend:send task enqueue.
func WithTestSendEnqueuer(e TestSendEnqueuer) ServiceOption {
	return func(s *Service) { s.testSendEnq = e }
}

// WithRateLimiter wires test-send's abuse guard. Without it, test-send is
// unlimited (see Service.checkTestSendRateLimit) -- a deployment choice made
// once at wiring time, not a silent bypass.
func WithRateLimiter(l RateLimiter) ServiceOption { return func(s *Service) { s.limiter = l } }

// WithEvents wires realtime announcements. Omitting it (or passing nil) leaves
// the service silent, which is the pre-socket behaviour: clients learn about a
// launch on their next refetch.
func WithEvents(p events.Publisher) ServiceOption { return func(s *Service) { s.events = p } }

// WithSuppressionChecker wires test-send's suppression-list guard: a test
// email must never go to an address the workspace has explicitly
// unsubscribed or bounced. Without it, TestSend skips the check (see
// Service.checkRecipientNotSuppressed) -- cmd/inroad always wires one in
// production; internal/worker/testsend re-checks independently before
// dialing as defense in depth.
func WithSuppressionChecker(c SuppressionChecker) ServiceOption {
	return func(s *Service) { s.suppression = c }
}

func NewService(store Store, checker Checker, opts ...ServiceOption) *Service {
	s := &Service{store: store, checker: checker, metrics: newMetricsCache(metricsCacheTTL)}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Create verifies the mailbox is active and the list exists in the
// workspace before persisting the campaign.
func (s *Service) Create(ctx context.Context, ws uuid.UUID, in CreateInput) (gen.Campaign, error) {
	active, err := s.checker.MailboxActive(ctx, ws, in.MailboxID)
	if err != nil {
		return gen.Campaign{}, err
	}
	if !active {
		return gen.Campaign{}, ErrMailboxNotActive
	}
	exists, err := s.checker.ListExists(ctx, ws, in.ListID)
	if err != nil {
		return gen.Campaign{}, err
	}
	if !exists {
		return gen.Campaign{}, ErrListMissing
	}
	return s.store.Create(ctx, ws, in)
}

// Get returns a single campaign, scoped to the workspace.
func (s *Service) Get(ctx context.Context, ws, id uuid.UUID) (gen.Campaign, error) {
	return s.store.Get(ctx, ws, id)
}

// List returns every campaign in the workspace.
func (s *Service) List(ctx context.Context, ws uuid.UUID) ([]gen.Campaign, error) {
	return s.store.List(ctx, ws)
}

// Stats returns send counts grouped by status for the campaign. The
// workspace id is included so a cross-tenant campaign id yields empty
// results rather than leaking counts (defense in depth on top of the
// ownership check the caller has already run via Get).
func (s *Service) Stats(ctx context.Context, ws, id uuid.UUID) (map[string]int64, error) {
	return s.store.Stats(ctx, ws, id)
}

// CampaignDetail is the extended GET /campaigns/{id} payload: the campaign, its
// ordered steps, send counts by status, enrollment counts by status, and the
// engagement Metrics rollup.
type CampaignDetail struct {
	Campaign    gen.Campaign
	Steps       []gen.SequenceStep
	SendStats   map[string]int64
	Enrollments map[string]int64
	Metrics     Metrics
}

// Metrics is the per-campaign engagement rollup shown on GET /campaigns/{id}.
// Counts are raw aggregates. Rates use TWO different denominators, guarded to
// 0 when their denominator is 0 rather than dividing by zero:
//   - OpenRate/ClickRate = OpensIndicative|Clicks / Sent (per-send: a
//     multi-step campaign sends several times per contact, and opens/clicks
//     are tracked per send).
//   - ReplyRate/BounceRate/UnsubRate = Replies|Bounces|Unsubscribes /
//     totalEnrolled (per-contact: an enrollment stops at most once, so
//     dividing by the per-send Sent count would read ~Nx low on an N-step
//     campaign). totalEnrolled is the sum of Enrollments (each row is one
//     contact's enrollment, exactly one per contact for the campaign's
//     lifetime -- active + completed + stopped).
//
// OpensIndicative and Clicks both count only HUMAN-classified events: each
// tracking hit is judged once at write time by platform/botfilter (known proxy
// and scanner UAs, the sub-2s prefetch window, a click with no preceding open,
// datacenter ranges, per-subnet bursts) and the queries filter on that stored
// verdict. Clicks are filtered too now -- a link scanner that follows every URL
// in a message used to count as a click while the open side excluded proxies,
// which could report more clicks than opens.
//
// It remains an APPROXIMATION, and deliberately errs toward counting a doubtful
// hit as human: over-filtering silently deletes a real person's engagement,
// while under-filtering only inflates a number an operator can question. The
// machine events are not discarded -- CountTrackingEventsByKindAndVerdict can
// report "N opens, M of them machine" -- so the excluded volume stays visible
// rather than vanishing.
type Metrics struct {
	Sent            int64
	OpensIndicative int64
	Clicks          int64
	Replies         int64
	Bounces         int64
	Unsubscribes    int64

	OpenRate   float64
	ClickRate  float64
	ReplyRate  float64
	BounceRate float64
	UnsubRate  float64
}

// stop_reason values that feed the Metrics rollup. Duplicated as plain
// strings (rather than importing internal/app/enrollment's StopReason
// constants) because app/* packages must not import each other -- see
// internal/app/enrollment/status.go for the canonical definitions these
// mirror.
const (
	stopReasonReplied    = "replied"
	stopReasonBounced    = "bounced"
	stopReasonSuppressed = "suppressed"
)

// computeMetrics turns raw counts into a Metrics snapshot. sent (per-send)
// and totalEnrolled (per-contact) are independent denominators -- see the
// Metrics doc comment -- each guarded to 0 rather than dividing by zero.
func computeMetrics(sent, totalEnrolled, opens, clicks int64, stopReasons map[string]int64) Metrics {
	m := Metrics{
		Sent: sent, OpensIndicative: opens, Clicks: clicks,
		Replies:      stopReasons[stopReasonReplied],
		Bounces:      stopReasons[stopReasonBounced],
		Unsubscribes: stopReasons[stopReasonSuppressed],
	}
	if sent > 0 {
		total := float64(sent)
		m.OpenRate = float64(m.OpensIndicative) / total
		m.ClickRate = float64(m.Clicks) / total
	}
	if totalEnrolled > 0 {
		enrolled := float64(totalEnrolled)
		m.ReplyRate = float64(m.Replies) / enrolled
		m.BounceRate = float64(m.Bounces) / enrolled
		m.UnsubRate = float64(m.Unsubscribes) / enrolled
	}
	return m
}

// sumCounts adds up every value in a status/reason -> count map, e.g. to
// turn Enrollments (grouped by lifecycle status) into a total enrolled-contact
// count (each enrollment row is exactly one contact, for the campaign's
// lifetime).
func sumCounts(counts map[string]int64) int64 {
	var total int64
	for _, n := range counts {
		total += n
	}
	return total
}

// metricsCacheTTL bounds how long the raw engagement aggregates (opens,
// clicks, stop-reason counts) are served from cache before being recomputed.
// Those three queries touch tracking_events and sequence_enrollments
// (COUNT(DISTINCT)/GROUP BY reads); a dashboard polling GET /campaigns/{id}
// every few seconds would otherwise re-run all of them on every load.
// Sent (and therefore every rate) is NOT cached -- it's recomputed from the
// always-fresh Stats() call on every request, so Metrics.Sent never diverges
// from the response's top-level stats.sent. Tradeoff: OpensIndicative/Clicks/
// Replies/Bounces/Unsubscribes (the counts, not the rates) can lag the true
// values by up to this TTL. A rollup table is the next-step scale path if
// this cache isn't enough; out of scope here.
const metricsCacheTTL = 45 * time.Second

// rawEngagement holds the query-heavy aggregates that back Metrics, cached
// independently of Sent (see metricsCacheTTL).
type rawEngagement struct {
	opens, clicks int64
	stopReasons   map[string]int64
}

// metricsCacheEntry pairs a raw aggregate snapshot with its expiry.
type metricsCacheEntry struct {
	raw     rawEngagement
	expires time.Time
}

// metricsCache is a mutex-guarded, per-campaign TTL cache for the
// query-heavy engagement aggregates (opens/clicks/stop-reasons). Deliberately
// minimal: no eviction or background sweep, entries are simply overwritten on
// recompute. The map is bounded by the number of distinct campaigns viewed
// within the TTL window, which is small relative to the cost of
// re-aggregating on every dashboard poll.
type metricsCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	now     func() time.Time
	entries map[[2]uuid.UUID]metricsCacheEntry
}

func newMetricsCache(ttl time.Duration) *metricsCache {
	return &metricsCache{ttl: ttl, now: time.Now, entries: make(map[[2]uuid.UUID]metricsCacheEntry)}
}

func (c *metricsCache) get(ws, id uuid.UUID) (rawEngagement, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[[2]uuid.UUID{ws, id}]
	if !ok || c.now().After(e.expires) {
		return rawEngagement{}, false
	}
	return e.raw, true
}

func (c *metricsCache) set(ws, id uuid.UUID, raw rawEngagement) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[[2]uuid.UUID{ws, id}] = metricsCacheEntry{raw: raw, expires: c.now().Add(c.ttl)}
}

// Detail loads the campaign plus its steps, rollup counts, and engagement
// metrics, all workspace-scoped (a cross-tenant id yields ErrNotFound before
// any child read). The query-heavy engagement aggregates (opens/clicks/
// stop-reasons) are served from a short-TTL cache (metricsCacheTTL) so
// repeated dashboard loads don't re-run those queries every time; Sent,
// totalEnrolled (sumCounts(enr)), and every rate are always recomputed from
// the fresh Stats()/EnrollmentCounts() calls so Metrics.Sent can never
// diverge from the response's top-level stats.sent.
func (s *Service) Detail(ctx context.Context, ws, id uuid.UUID) (CampaignDetail, error) {
	c, err := s.store.Get(ctx, ws, id)
	if err != nil {
		return CampaignDetail{}, ErrNotFound
	}
	steps, err := s.store.ListSteps(ctx, ws, id)
	if err != nil {
		return CampaignDetail{}, err
	}
	sends, err := s.store.Stats(ctx, ws, id)
	if err != nil {
		return CampaignDetail{}, err
	}
	enr, err := s.store.EnrollmentCounts(ctx, ws, id)
	if err != nil {
		return CampaignDetail{}, err
	}
	raw, ok := s.metrics.get(ws, id)
	if !ok {
		opens, clicks, err := s.store.EngagementCounts(ctx, ws, id)
		if err != nil {
			return CampaignDetail{}, err
		}
		stopReasons, err := s.store.StopReasonCounts(ctx, ws, id)
		if err != nil {
			return CampaignDetail{}, err
		}
		raw = rawEngagement{opens: opens, clicks: clicks, stopReasons: stopReasons}
		s.metrics.set(ws, id, raw)
	}
	metrics := computeMetrics(sends["sent"], sumCounts(enr), raw.opens, raw.clicks, raw.stopReasons)
	return CampaignDetail{Campaign: c, Steps: steps, SendStats: sends, Enrollments: enr, Metrics: metrics}, nil
}

// Enrollment-listing pagination bounds: default page size when the caller
// supplies none (or a non-positive value) and the hard ceiling per page.
const (
	defaultEnrollmentLimit = 100
	maxEnrollmentLimit     = 500
)

// ListEnrollments returns per-contact reply status for the campaign, paginated.
// The campaign's workspace ownership is verified first (a cross-tenant id yields
// ErrNotFound before any enrollment read), and the SQL is itself workspace-pinned
// as defense in depth. limit is clamped to [1,maxEnrollmentLimit] with a default
// of defaultEnrollmentLimit for non-positive input; a negative offset is floored
// to 0.
func (s *Service) ListEnrollments(ctx context.Context, ws, id uuid.UUID, limit, offset int32) ([]gen.ListCampaignEnrollmentsRow, error) {
	if _, err := s.store.Get(ctx, ws, id); err != nil {
		return nil, ErrNotFound
	}
	switch {
	case limit <= 0:
		limit = defaultEnrollmentLimit
	case limit > maxEnrollmentLimit:
		limit = maxEnrollmentLimit
	}
	if offset < 0 {
		offset = 0
	}
	return s.store.ListEnrollments(ctx, ws, id, limit, offset)
}

// SetTracking flips the campaign's tracking-enabled flag, workspace-scoped.
// Editable regardless of campaign status: tracking only affects sends going
// out after the flag changes, so there's no reason to restrict it to draft.
func (s *Service) SetTracking(ctx context.Context, ws, id uuid.UUID, enabled bool) error {
	if _, err := s.store.Get(ctx, ws, id); err != nil {
		return ErrNotFound
	}
	return s.store.SetTracking(ctx, ws, id, enabled)
}

// LaunchResult reports the outcome of a Launch call. TotalEnrolled is the
// number of enrollments the DB transaction created; EnqueuedCount and
// FailedEnqueueCount split that total by whether each enrollment's step-1
// advance made it onto the queue. A non-zero FailedEnqueueCount is not a hard
// failure — the enrollment sweeper reconciles unqueued rows on its next tick —
// but the counts are surfaced so callers can log/alert.
type LaunchResult struct {
	TotalEnrolled      int
	EnqueuedCount      int
	FailedEnqueueCount int
	// LastScheduledAt is when the final send of the launch is scheduled. A narrow
	// window and a large list can push it days out; surfacing it lets the UI warn
	// rather than leaving the operator to discover it from an idle campaign.
	LastScheduledAt time.Time
}

// Launch transitions a draft campaign to running: it materializes one
// enrollment per list member and flips the campaign status atomically (via
// store.EnrollTx), then stagger-schedules a sequence:advance task for every new
// enrollment (setting its next_due_at to match, so the sweeper won't fire it
// early). The lazy chain enqueues each subsequent step after the prior sends.
//
// Enqueue errors are counted, not swallowed: the DB writes are already
// committed, so rolling back would drop legitimate work; the enrollment sweeper
// (queue.TaskSweepEnrollments) re-enqueues any orphaned enrollments next tick.
func (s *Service) Launch(ctx context.Context, ws, campaignID uuid.UUID, enq Enqueuer) (LaunchResult, error) {
	c, err := s.store.Get(ctx, ws, campaignID)
	if err != nil {
		return LaunchResult{}, ErrNotFound
	}
	if c.Status != string(StatusDraft) {
		return LaunchResult{}, ErrAlreadyLaunched
	}
	steps, err := s.store.CountSteps(ctx, ws, campaignID)
	if err != nil {
		return LaunchResult{}, err
	}
	if steps == 0 {
		return LaunchResult{}, ErrNoSteps
	}
	enrollments, err := s.store.EnrollTx(ctx, ws, campaignID)
	if err != nil {
		return LaunchResult{}, err
	}
	if len(enrollments) == 0 {
		return LaunchResult{}, ErrEmptyList
	}
	res := LaunchResult{TotalEnrolled: len(enrollments)}
	// EnrollTx has committed, so the campaign is launched from here on — every
	// exit below is a success as far as an observer is concerned, and each one
	// announces it. Emitting once here rather than at each return would be
	// wrong in the other direction: the two error paths below still leave a
	// running campaign, and a client that never heard about it would show a
	// draft until the next refetch.
	s.announceLaunched(ctx, ws, campaignID, len(enrollments))

	due, err := s.spread(ctx, ws, campaignSchedule{id: campaignID, timezone: c.Timezone}, enrollments)
	if err != nil {
		// The enrollments are committed; without due times they'd all be due
		// immediately, which is the burst the cadence spread exists to prevent.
		// Surface it — the campaign is running and the sweeper will pick the rows
		// up, but the operator must know the schedule was not applied.
		return res, err
	}
	if err := s.store.RescheduleBatch(ctx, ws, due); err != nil {
		return res, err
	}
	for _, e := range enrollments {
		// Enqueue each advance at exactly the instant just stamped on its
		// enrollment, so the scheduled task and the enrollment's due cursor are
		// identical by construction. A failed enqueue is non-fatal — the
		// enrollment sweeper reconciles it next tick.
		if err := enq.EnqueueAdvanceAt(e.ID.String(), ws.String(), due[e.ID]); err != nil {
			res.FailedEnqueueCount++
			continue
		}
		res.EnqueuedCount++
		res.LastScheduledAt = maxTime(res.LastScheduledAt, due[e.ID])
	}
	return res, nil
}

// announceLaunched tells the workspace's open tabs a campaign started sending.
//
// Ids and counts only: a client refetches the campaign through the normal
// authorized endpoint, so this cannot become a way around the checks that
// endpoint applies. The enrolled count rides along because it is the one number
// a launch toast wants and it reveals nothing a list view would not.
func (s *Service) announceLaunched(ctx context.Context, ws, campaignID uuid.UUID, enrolled int) {
	events.Emit(ctx, s.events, ws.String(), events.Event{
		Type:        "campaign.launched",
		SubjectKind: "campaign",
		SubjectID:   campaignID.String(),
		ActorID:     events.ActorFrom(ctx),
		OccurredAt:  time.Now().UTC(),
		Data:        map[string]any{"campaign_id": campaignID.String(), "enrolled": enrolled},
	})
}

// spread assigns each new enrollment its own send instant: offsets drawn through
// the cadence distribution curve, resolved against the campaign's send window so
// a batch larger than one day's open time rolls into the following days rather
// than spilling past a window edge.
//
// Keyed on the enrollment id, so a retried launch recomputes identical instants.
func (s *Service) spread(ctx context.Context, ws uuid.UUID, cs campaignSchedule, enrollments []Enrollment) (map[uuid.UUID]time.Time, error) {
	win, err := s.window(ctx, ws, cs)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	// Anchor on the first open instant at or after now, so offsets are measured
	// from when sending can actually start rather than from a closed window.
	start, err := win.Next(now, cs.id.String())
	if err != nil {
		return nil, err
	}
	open := win.OpenDuration(start)
	offsets := cadence.Offsets(open, len(enrollments), cs.id.String())

	due := make(map[uuid.UUID]time.Time, len(enrollments))
	for i, e := range enrollments {
		offset := time.Duration(0)
		if i < len(offsets) {
			offset = offsets[i]
		}
		at, err := win.NextAfterOffset(start, offset, e.ID.String())
		if err != nil {
			return nil, err
		}
		due[e.ID] = at
	}
	return due, nil
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
