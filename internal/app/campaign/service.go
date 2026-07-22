package campaign

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
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
type Service struct {
	store   Store
	checker Checker
	metrics *metricsCache
}

func NewService(store Store, checker Checker) *Service {
	return &Service{store: store, checker: checker, metrics: newMetricsCache(metricsCacheTTL)}
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
// OpensIndicative is proxy-filtered (CountHumanOpens excludes known
// prefetch UAs and near-instant fetches) but remains an approximation --
// clicks are the reliable signal.
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
	for _, e := range enrollments {
		// EnrollListMembers already staggered next_due_at at insert time; we
		// enqueue each advance at exactly that DB-assigned time (asynq needs one
		// task per enrollment) so the scheduled task and the enrollment's due
		// cursor are identical by construction — never recompute the stagger in
		// Go, since RETURNING row order isn't guaranteed to match the window
		// ORDER BY. A failed enqueue is non-fatal — the enrollment sweeper
		// reconciles it next tick.
		if err := enq.EnqueueAdvanceAt(e.ID.String(), ws.String(), e.NextDueAt); err != nil {
			res.FailedEnqueueCount++
			continue
		}
		res.EnqueuedCount++
	}
	return res, nil
}
