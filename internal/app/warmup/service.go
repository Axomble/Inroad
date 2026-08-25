package warmup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	pwarmup "github.com/inroad/inroad/internal/platform/warmup"
)

// Sentinel errors the handler layer maps to HTTP status codes.
var (
	// ErrNotFound is returned when the target mailbox is not owned by the
	// workspace, or (for detail) is not a warmup participant. The handler maps
	// it to 404 so a caller can never distinguish "not yours" from "not
	// enrolled" — both are simply absent from this workspace's view.
	ErrNotFound = errors.New("warmup: mailbox not found")
	// ErrValidation is returned when the requested ramp settings violate the
	// boundary rules (1 <= start <= max <= 200, increment >= 1, 0 <= reply <= 1).
	ErrValidation = errors.New("warmup: invalid settings")
)

// Ramp setting bounds and defaults (spec §3 / §10). Defaults seed the ramp when
// a mailbox is first enabled with omitted fields; the bounds gate every update.
const (
	defaultStartVolume   int32   = 4
	defaultMaxVolume     int32   = 40
	defaultRampIncrement int32   = 2
	defaultReplyRate     float32 = 0.30

	maxVolumeCeiling int32 = 200
)

// Transition history page bounds, mirroring the listWarmupTransitions contract
// (limit: minimum 1, maximum 200, default 50).
const (
	defaultTransitionLimit int32 = 50
	maxTransitionLimit     int32 = 200
)

// healthPaused is the one health_state that zeroes today's target: a paused
// participant sends nothing until it recovers (spec §8).
const healthPaused = "paused"

// Service implements the warmup control-plane use cases. It depends on the
// consumer-defined Store interface (never the concrete PgStore) so it unit-tests
// against a fake with no database. now is injectable purely so the ramp-from
// started_at math is deterministic under test; production wires time.Now.
type Service struct {
	store Store
	now   func() time.Time
}

// NewService builds the warmup service over a Store. The clock defaults to
// time.Now; tests override svc.now to pin the ramp day.
func NewService(store Store) *Service {
	return &Service{store: store, now: time.Now}
}

// WarmupSettings is the enable/update request. Every field is optional (a nil
// pointer): on a first enable an omitted field takes the package default, on an
// update it keeps the participant's current value (spec §10). Validation runs on
// the RESOLVED values, after defaults/current are filled in.
type WarmupSettings struct {
	StartVolume   *int32
	MaxVolume     *int32
	RampIncrement *int32
	ReplyRate     *float32
}

// resolvedSettings are the concrete ramp values after merging a request over the
// current-or-default base.
type resolvedSettings struct {
	startVolume   int32
	maxVolume     int32
	rampIncrement int32
	replyRate     float32
}

func (r resolvedSettings) validate() error {
	switch {
	case r.startVolume < 1:
		return fmt.Errorf("%w: start_volume must be >= 1", ErrValidation)
	case r.maxVolume > maxVolumeCeiling:
		return fmt.Errorf("%w: max_volume must be <= %d", ErrValidation, maxVolumeCeiling)
	case r.startVolume > r.maxVolume:
		return fmt.Errorf("%w: start_volume must be <= max_volume", ErrValidation)
	case r.rampIncrement < 1:
		return fmt.Errorf("%w: ramp_increment must be >= 1", ErrValidation)
	case r.replyRate < 0 || r.replyRate > 1:
		return fmt.Errorf("%w: reply_rate must be in [0,1]", ErrValidation)
	}
	return nil
}

// EnableWarmup enables warmup for a mailbox or updates its ramp settings. Omitted
// request fields keep the participant's current value (or the package default on
// a first enable). Validation is enforced at this boundary. Ownership is enforced
// by the store's self-enforcing upsert: a mailbox not in the workspace writes no
// row (ErrMailboxNotInWorkspace), which is surfaced as ErrNotFound so the handler
// 404s. The returned DTO carries the computed today_sent / today_target.
func (s *Service) EnableWarmup(ctx context.Context, ws, mailboxID uuid.UUID, settings WarmupSettings) (WarmupParticipantDTO, error) {
	base, err := s.currentOrDefault(ctx, ws, mailboxID)
	if err != nil {
		return WarmupParticipantDTO{}, err
	}
	resolved := merge(base, settings)
	if err := resolved.validate(); err != nil {
		return WarmupParticipantDTO{}, err
	}

	p, err := s.store.UpsertParticipant(ctx, UpsertParams{
		MailboxID:     mailboxID,
		WorkspaceID:   ws,
		StartVolume:   resolved.startVolume,
		MaxVolume:     resolved.maxVolume,
		RampIncrement: resolved.rampIncrement,
		ReplyRate:     resolved.replyRate,
	})
	switch {
	case errors.Is(err, ErrMailboxNotInWorkspace):
		return WarmupParticipantDTO{}, ErrNotFound
	case err != nil:
		return WarmupParticipantDTO{}, err
	}

	sent, err := s.store.SentToday(ctx, ws, mailboxID)
	if err != nil {
		return WarmupParticipantDTO{}, err
	}
	return s.participantDTO(p, sent), nil
}

// currentOrDefault reads the participant's existing settings as the merge base,
// falling back to the package defaults ONLY when the mailbox is not yet a
// participant (pgx.ErrNoRows — also the case for a foreign mailbox, which reads
// as absent here and is caught downstream by the self-enforcing upsert). Any
// OTHER read error is propagated, never swallowed: on a partial update of an
// EXISTING participant a transient read failure must NOT silently collapse the
// merge base to defaults, or the ON CONFLICT DO UPDATE would overwrite the live
// start/max/increment/reply settings back to defaults (silent data corruption).
func (s *Service) currentOrDefault(ctx context.Context, ws, mailboxID uuid.UUID) (resolvedSettings, error) {
	cur, err := s.store.GetParticipant(ctx, ws, mailboxID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return resolvedSettings{
			startVolume:   defaultStartVolume,
			maxVolume:     defaultMaxVolume,
			rampIncrement: defaultRampIncrement,
			replyRate:     defaultReplyRate,
		}, nil
	case err != nil:
		return resolvedSettings{}, fmt.Errorf("warmup: read current settings: %w", err)
	}
	return resolvedSettings{
		startVolume:   cur.StartVolume,
		maxVolume:     cur.MaxVolume,
		rampIncrement: cur.RampIncrement,
		replyRate:     cur.ReplyRate,
	}, nil
}

// merge overlays the non-nil request fields onto the base settings.
func merge(base resolvedSettings, req WarmupSettings) resolvedSettings {
	if req.StartVolume != nil {
		base.startVolume = *req.StartVolume
	}
	if req.MaxVolume != nil {
		base.maxVolume = *req.MaxVolume
	}
	if req.RampIncrement != nil {
		base.rampIncrement = *req.RampIncrement
	}
	if req.ReplyRate != nil {
		base.replyRate = *req.ReplyRate
	}
	return base
}

// DisableWarmup removes the mailbox from warmup. It is idempotent: disabling a
// mailbox that was never enrolled (zero rows) is a success, matching the 204
// contract.
func (s *Service) DisableWarmup(ctx context.Context, ws, mailboxID uuid.UUID) error {
	_, err := s.store.DisableParticipant(ctx, ws, mailboxID)
	return err
}

// GetWarmupDetail returns one mailbox's participant state plus its up-to-30-day
// daily series. A mailbox that is not a participant in this workspace is
// ErrNotFound (404).
func (s *Service) GetWarmupDetail(ctx context.Context, ws, mailboxID uuid.UUID) (WarmupDetailDTO, error) {
	p, err := s.store.GetParticipant(ctx, ws, mailboxID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return WarmupDetailDTO{}, ErrNotFound
	case err != nil:
		return WarmupDetailDTO{}, err
	}
	sent, err := s.store.SentToday(ctx, ws, mailboxID)
	if err != nil {
		return WarmupDetailDTO{}, err
	}
	stats, err := s.store.DailyStats(ctx, ws, mailboxID)
	if err != nil {
		return WarmupDetailDTO{}, err
	}
	series := make([]WarmupDayStatDTO, len(stats))
	for i, d := range stats {
		series[i] = dayStatDTO(d)
	}
	routes, err := s.store.ListRoutes(ctx, ws, mailboxID)
	if err != nil {
		return WarmupDetailDTO{}, err
	}
	// make, not a nil slice: `routes` is always present and `[]` when nothing was
	// observed, because an absent key arrives as undefined and sends a client down a
	// fallback path that looks identical to "no mail was sent".
	matrix := make([]WarmupRouteDTO, len(routes))
	for i, r := range routes {
		matrix[i] = routeDTO(r)
	}
	return WarmupDetailDTO{Participant: s.participantDTO(p, sent), Series: series, Routes: matrix}, nil
}

// routeDTO maps one destination route onto the wire shape, applying the sample
// floor to its own denominator.
//
// A route below pwarmup.MinPlacementSamples reports its counts and NO rates. The
// EXISTING constant is reused rather than a per-route minimum invented: the
// question "is this rate proven" does not change because the population was split,
// and a second threshold would be a second thing to keep in step with the first.
//
// It maps only; it decides nothing. No caller may branch a health state, lane or
// promotion on the result — and the reason differs from the tabbed rate's, which
// is why it is written here rather than assumed. A tab is structurally
// unobservable over IMAP, so gating it would penalise a whole provider class
// permanently. A per-route rate is observable everywhere the route exists; what is
// missing is CALIBRATION, and that expires. Before wiring a threshold to one of
// these, read design §7 — the distinction is the reason this slice ships before the
// one that would consume it.
func routeDTO(r RouteRow) WarmupRouteDTO {
	sample := r.Inbox7d + r.Spam7d
	route := WarmupRouteDTO{
		DestinationESP:     r.DestinationESP,
		PlacementSample7d:  sample,
		TabCapableSample7d: r.TabCapable7d,
	}
	if sample < int64(pwarmup.MinPlacementSamples) {
		return route // not established: the counts are reported, the rates are null
	}
	route.InboxRate7d = placementRate(r.Inbox7d, sample)
	route.SpamRate7d = placementRate(r.Spam7d, sample)
	// The tabbed rate keeps its own denominator INSIDE the route, for the reason it
	// has one at all: observations whose reader could never name a tab must not
	// dilute it toward zero and make an untested route read clean.
	route.TabbedRate7d = placementRate(r.Tabbed7d, r.TabCapable7d)
	return route
}

// GetOverview returns the workspace warmup summary: pool size (enabled
// participants), whether warmup is active (pool_size >= 2, the minimum to
// exchange mail), and per-mailbox health + placement. Everything is resolved from
// one workspace-pinned overview read plus the enabled count.
func (s *Service) GetOverview(ctx context.Context, ws uuid.UUID) (WarmupOverviewDTO, error) {
	count, err := s.store.CountEnabledParticipants(ctx, ws)
	if err != nil {
		return WarmupOverviewDTO{}, err
	}
	rows, err := s.store.ListOverviewRows(ctx, ws)
	if err != nil {
		return WarmupOverviewDTO{}, err
	}
	now := s.now()
	mailboxes := make([]WarmupMailboxDTO, len(rows))
	for i, r := range rows {
		placementSample := r.Inbox7d + r.Spam7d
		mailboxes[i] = WarmupMailboxDTO{
			MailboxID:         r.MailboxID.String(),
			Email:             r.Email,
			Enabled:           r.Enabled,
			HealthState:       r.HealthState,
			HealthReason:      r.HealthReason,
			Lane:              r.Lane,
			LaneReason:        r.LaneReason,
			TodaySent:         r.TodaySent,
			TodayTarget:       targetFor(r.HealthState, r.StartVolume, r.MaxVolume, r.RampIncrement, r.StartedAt, now),
			PlacementSample7d: placementSample,
			// The tabbed rate is measured over its OWN denominator: only the
			// observations whose reader could have named a tab. Pooling the rest would
			// dilute it toward zero, so a pool of IMAP mailboxes — most of a self-hosted
			// deployment — would report a clean tab rate it never measured.
			TabbedRate7d:       placementRate(r.Tabbed7d, r.TabCapable7d),
			TabCapableSample7d: r.TabCapable7d,
			InboxRate7d:        placementRate(r.Inbox7d, placementSample),
			SpamRate7d:         placementRate(r.Spam7d, placementSample),
			Identity:           identityDTO(r),
		}
	}
	return WarmupOverviewDTO{
		PoolSize:            int(count),
		Active:              count >= 2,
		Mailboxes:           mailboxes,
		DiscountedObservers: s.discountedObservers(ctx, ws),
		Incidents:           s.incidents(ctx, ws),
		IncidentsMinPool:    pwarmup.MinIncidentPool,
	}, nil
}

// discountedObservers renders the observers whose placement reports the snapshot
// refresh is excluding, arithmetic included.
//
// This is the operator-visible half of the ONLY inference in this subsystem that
// gates. The refresh discards these observers' reports, so the rates on the mailbox
// rows beside this list are computed WITHOUT them; publishing the list is what keeps
// that from being a silent correction.
//
// IT RETURNS NO ERROR, for the same reason incidents does not: a read that cannot
// compute an inference must not take the pool summary, the health states and the
// placement rates down with it. The conflation with "every observer is trusted" is
// the accepted cost — and it is the honest direction here, because a failed read on
// the WRITE side falls back to exactly that, an empty exclusion list.
func (s *Service) discountedObservers(ctx context.Context, ws uuid.UUID) []WarmupDiscountedObserverDTO {
	stats, err := s.store.ListObserverStats(ctx, ws)
	if err != nil {
		slog.ErrorContext(ctx, "warmup_observer_trust_read_failed", "workspace_id", ws, "err", err)
		return []WarmupDiscountedObserverDTO{}
	}
	found := pwarmup.DiscountObservers(stats)
	// make, not a nil slice: `discounted_observers` is always present and `[]` when
	// nothing was discounted, because an absent key arrives as undefined and sends a
	// client down a fallback path indistinguishable from "the check did not run".
	out := make([]WarmupDiscountedObserverDTO, len(found))
	for i, in := range found {
		out[i] = discountedObserverDTO(in)
	}
	return out
}

// discountedObserverDTO maps one discounted observer onto the wire shape. The order
// it arrives in is the detector's (worst lift first, then a total order) and is NOT
// re-sorted here, exactly as incidentDTO's is.
func discountedObserverDTO(in pwarmup.DiscountedObserver) WarmupDiscountedObserverDTO {
	return WarmupDiscountedObserverDTO{
		ObserverMailboxID: in.ObserverMailboxID,
		Cohort:            in.Cohort,
		Spam:              in.Spam,
		Total:             in.Total,
		// The two rates are trimmed with roundLift rather than wireRate: they are not
		// stored REALs being widened for a client to compare against a threshold, they
		// are ratios of small integer counts computed here, and two decimals is the
		// precision the counts support. The same call for all three also keeps the row
		// internally consistent — a lift printed to 2dp beside rates printed to 6
		// invites a reader to check the division and find it "wrong".
		SpamRate:       roundLift(in.SpamRate),
		CohortSpamRate: roundLift(in.CohortSpamRate),
		Lift:           roundLift(in.Lift),
	}
}

// incidents detects correlated degradation across the pool's fault dimensions and
// renders it, arithmetic included.
//
// IT RETURNS NO ERROR, and that is the design (§9, last bullet). The overview is the
// operator's window into a degrading pool, and it must not go dark because an
// INFERENCE over data it is already showing could not be computed. A failure is
// logged and degrades to "no incidents" — which reads as "nothing shared was found",
// the same as a healthy pool. That conflation is the accepted cost: the alternative
// is a 500 that hides the mailbox rows, the health states and the placement rates
// too, all of which are still true.
func (s *Service) incidents(ctx context.Context, ws uuid.UUID) []WarmupIncidentDTO {
	participants, err := s.store.ListIncidentParticipants(ctx, ws)
	if err != nil {
		slog.ErrorContext(ctx, "warmup_incident_detection_failed", "workspace_id", ws, "err", err)
		return []WarmupIncidentDTO{}
	}
	found := pwarmup.DetectIncidents(participants)
	// make, not a nil slice: `incidents` is always present and `[]` when nothing
	// correlated, because an absent key arrives as undefined and sends a client down a
	// fallback path that looks identical to "detection did not run".
	out := make([]WarmupIncidentDTO, len(found))
	for i, in := range found {
		out[i] = incidentDTO(in)
	}
	return out
}

// incidentDTO maps one detected correlation onto the wire shape. The order it
// arrives in is the detector's (strongest lift first, then a total order) and is NOT
// re-sorted here: a second opinion about which finding matters most is exactly the
// kind of duplicated ranking this subsystem keeps having to remove.
func incidentDTO(in pwarmup.Incident) WarmupIncidentDTO {
	// Members are always a list, never null: an incident with no named members cannot
	// occur (MinIncidentMembers is 2), so a nil here would only ever be a mapping bug
	// rendered as an empty state.
	members := in.Members
	if members == nil {
		members = []string{}
	}
	return WarmupIncidentDTO{
		Dimension:        in.Dimension,
		Value:            in.Value,
		MemberMailboxIDs: members,
		CohortSize:       in.CohortSize,
		DegradedInside:   in.DegradedInside,
		CohortOutside:    in.CohortOutside,
		DegradedOutside:  in.DegradedOutside,
		Lift:             roundLift(in.Lift),
	}
}

// roundLift trims a lift to two decimals for the wire.
//
// Separate from wireRate, which widens a stored float32 REAL and needs six decimals
// to stay finer than the smallest threshold the policy compares against. A lift
// crosses no threshold and is an estimate over counts that are frequently single
// digits: two decimals distinguishes the findings that differ (2.1 from 12) and
// declines to publish twelve more digits the pool cannot support.
func roundLift(lift float64) float64 {
	return math.Round(lift*100) / 100
}

// ListTransitions returns one mailbox's automated decision history, newest
// first. A mailbox that is not this workspace's is ErrNotFound (404); a mailbox
// that is simply not (or no longer) a warmup participant is a 200 with whatever
// history it accumulated, because the trail deliberately OUTLIVES the participant
// row — that is how containment survives a disable/re-enable, and hiding it would
// hide the reason a re-enabled mailbox came back quarantined.
func (s *Service) ListTransitions(ctx context.Context, ws, mailboxID uuid.UUID, limit int32) (WarmupTransitionPageDTO, error) {
	owned, err := s.store.MailboxInWorkspace(ctx, ws, mailboxID)
	if err != nil {
		return WarmupTransitionPageDTO{}, err
	}
	if !owned {
		return WarmupTransitionPageDTO{}, ErrNotFound
	}
	rows, err := s.store.ListTransitions(ctx, ws, mailboxID, clampTransitionLimit(limit))
	if err != nil {
		return WarmupTransitionPageDTO{}, err
	}
	out := make([]WarmupTransitionDTO, len(rows))
	for i, r := range rows {
		out[i] = transitionDTO(r)
	}
	return WarmupTransitionPageDTO{Transitions: out}, nil
}

// clampTransitionLimit applies the contract's bounds (1..200, default 50). It
// clamps rather than rejecting, because an out-of-range page size is a request
// for "as much as you'll give me", not a caller error worth a 4xx — and the cap
// exists to bound the server's work, which clamping achieves.
func clampTransitionLimit(limit int32) int32 {
	switch {
	case limit <= 0:
		return defaultTransitionLimit
	case limit > maxTransitionLimit:
		return maxTransitionLimit
	default:
		return limit
	}
}

// transitionDTO maps a persisted transition onto the wire shape.
func transitionDTO(t Transition) WarmupTransitionDTO {
	return WarmupTransitionDTO{
		ID:               t.ID.String(),
		CreatedAt:        rfc3339(t.CreatedAt),
		FromState:        t.FromState,
		ToState:          t.ToState,
		ReasonCode:       t.ReasonCode,
		Reason:           t.Reason,
		FromLane:         t.FromLane,
		ToLane:           t.ToLane,
		LaneReasonCode:   t.LaneReasonCode,
		LaneReason:       t.LaneReason,
		PlacementSamples: t.PlacementSamples,
		SpamRate:         wireRate(t.SpamRate),
		BouncePopulation: t.BouncePopulation,
		BounceSamples:    t.BounceSamples,
		BounceRate:       wireRate(t.BounceRate),
		ComplaintSamples: t.ComplaintSamples,
		ComplaintRate:    wireRate(t.ComplaintRate),
		InvalidTokens:    t.InvalidTokens,
		PolicyVersion:    t.PolicyVersion,
	}
}

// wireRate widens a stored REAL to the JSON number, rounding away the artefacts
// of the float32 round-trip: 0.15 stored as REAL widens to 0.15000000596046448,
// which is not a rate anyone measured. Six decimals is finer than the smallest
// threshold the policy uses (0.0003) by two orders of magnitude, so nothing
// meaningful is lost.
func wireRate(r float32) float64 {
	return math.Round(float64(r)*1e6) / 1e6
}

// participantDTO maps a persisted participant plus its computed today_sent into
// the WarmupParticipant wire shape, computing today_target from the ramp.
func (s *Service) participantDTO(p Participant, todaySent int32) WarmupParticipantDTO {
	return WarmupParticipantDTO{
		MailboxID:     p.MailboxID.String(),
		Enabled:       p.Enabled,
		StartVolume:   p.StartVolume,
		MaxVolume:     p.MaxVolume,
		RampIncrement: p.RampIncrement,
		ReplyRate:     p.ReplyRate,
		HealthState:   p.HealthState,
		HealthReason:  p.HealthReason,
		StartedAt:     rfc3339(p.StartedAt),
		TodaySent:     todaySent,
		TodayTarget:   targetFor(p.HealthState, p.StartVolume, p.MaxVolume, p.RampIncrement, p.StartedAt, s.now()),
	}
}

// targetFor computes today's ramp target: 0 while paused (spec §8), otherwise the
// pure ramp target for the number of whole UTC days the mailbox has been warming.
//
// This is the INTENDED daily ramp target — the clean, UN-JITTERED number we
// surface as today_target. The worker's per-day send cap is NOT identical: its
// NextDue applies DailyVolumeFactor (±~20% jitter) on top of this same ramp
// target, so the actual cap varies day to day. As a result today_sent can
// occasionally exceed today_target on a high-jitter day. Surfacing the clean
// ramp target (not a re-jittered value) keeps today_target stable and meaningful.
func targetFor(healthState string, start, maxVol, increment int32, startedAt pgtype.Timestamptz, now time.Time) int32 {
	if healthState == healthPaused {
		return 0
	}
	days := daysWarming(startedAt, now)
	return int32(pwarmup.RampTarget(int(start), int(maxVol), int(increment), days))
}

// daysWarming is the count of whole (24h) UTC days elapsed since started_at, day
// 0 being the start day itself. A missing or future start reads as day 0.
func daysWarming(startedAt pgtype.Timestamptz, now time.Time) int {
	if !startedAt.Valid {
		return 0
	}
	days := int(now.UTC().Sub(startedAt.Time.UTC()).Hours() / 24)
	if days < 0 {
		return 0
	}
	return days
}

// placementRate is n/denom as a fraction, guarding the empty-window divide-by-zero
// (no observed placements yet → rate 0).
func placementRate(n, denom int64) *float64 {
	if denom == 0 {
		return nil
	}
	rate := float64(n) / float64(denom)
	return &rate
}

// dayStatDTO maps a daily-stats row to the WarmupDayStat wire shape (day as a
// bare UTC date, YYYY-MM-DD).
func dayStatDTO(d DayStat) WarmupDayStatDTO {
	day := ""
	if d.Day.Valid {
		day = d.Day.Time.Format("2006-01-02")
	}
	return WarmupDayStatDTO{
		Day:      day,
		Sent:     d.Sent,
		Received: d.Received,
		Inbox:    d.Inbox,
		Spam:     d.Spam,
		Replies:  d.Replies,
	}
}

// identityDTO renders the row's identity facts, or nil when none were observed.
//
// The timestamp is the ONLY presence test, because the query COALESCEs the other
// five to their column defaults: two empty domains and three unknown verdicts is
// both what a LEFT JOIN miss produces and a perfectly legal observation of an
// unsigned message
// that no receiver reported on. Distinguishing them by inspecting those values
// would collapse "nobody has looked" into "we looked and saw nothing", which is
// exactly the absence-is-not-a-verdict discipline this signal is built on.
//
// It maps only; it decides nothing. No caller may branch a health state, lane or
// promotion on the result (design §7) — the verdicts are permanently unknown for
// any provider that stamps none, so gating on them would penalise a whole provider
// class for our inability to observe it, and DNS-verifiable authentication posture
// is already gated by sending_domains and the pending_auth lane.
func identityDTO(r OverviewRow) *WarmupIdentityDTO {
	if !r.IdentityObservedAt.Valid {
		return nil
	}
	return &WarmupIdentityDTO{
		DKIMDomain:       r.IdentityDKIMDomain,
		ReturnPathDomain: r.IdentityReturnPathDomain,
		SPFResult:        r.IdentitySPFResult,
		DKIMResult:       r.IdentityDKIMResult,
		DMARCResult:      r.IdentityDMARCResult,
		ObservedAt:       rfc3339(r.IdentityObservedAt),
	}
}

// rfc3339 renders a timestamptz as an RFC3339 UTC string ("" when unset).
func rfc3339(t pgtype.Timestamptz) string {
	if !t.Valid {
		return ""
	}
	return t.Time.UTC().Format(time.RFC3339)
}
