package warmup

import (
	"context"
	"errors"
	"fmt"
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
	return WarmupDetailDTO{Participant: s.participantDTO(p, sent), Series: series}, nil
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
		mailboxes[i] = WarmupMailboxDTO{
			MailboxID:    r.MailboxID.String(),
			Email:        r.Email,
			Enabled:      r.Enabled,
			HealthState:  r.HealthState,
			HealthReason: r.HealthReason,
			TodaySent:    r.TodaySent,
			TodayTarget:  targetFor(r.HealthState, r.StartVolume, r.MaxVolume, r.RampIncrement, r.StartedAt, now),
			InboxRate7d:  placementRate(r.Inbox7d, r.Inbox7d+r.Spam7d),
			SpamRate7d:   placementRate(r.Spam7d, r.Inbox7d+r.Spam7d),
		}
	}
	return WarmupOverviewDTO{
		PoolSize:  int(count),
		Active:    count >= 2,
		Mailboxes: mailboxes,
	}, nil
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
func placementRate(n, denom int64) float64 {
	if denom == 0 {
		return 0
	}
	return float64(n) / float64(denom)
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

// rfc3339 renders a timestamptz as an RFC3339 UTC string ("" when unset).
func rfc3339(t pgtype.Timestamptz) string {
	if !t.Valid {
		return ""
	}
	return t.Time.UTC().Format(time.RFC3339)
}
