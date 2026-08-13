package warmup

import (
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// Participant is the domain view of a warmup_participants row. It mirrors the
// persistence columns 1:1 (there is no secret to strip here, unlike mailbox),
// but exists so the domain surface returns a package-owned type instead of the
// generated gen.WarmupParticipant — the decoupling seam the service depends on.
type Participant struct {
	MailboxID     uuid.UUID
	WorkspaceID   uuid.UUID
	Enabled       bool
	StartVolume   int32
	MaxVolume     int32
	RampIncrement int32
	ReplyRate     float32
	StartedAt     pgtype.Timestamptz
	HealthState   string
	HealthReason  string
	PausedUntil   pgtype.Timestamptz
	CreatedAt     pgtype.Timestamptz
	UpdatedAt     pgtype.Timestamptz
}

func participantFromGen(p gen.WarmupParticipant) Participant {
	return Participant{
		MailboxID:     p.MailboxID,
		WorkspaceID:   p.WorkspaceID,
		Enabled:       p.Enabled,
		StartVolume:   p.StartVolume,
		MaxVolume:     p.MaxVolume,
		RampIncrement: p.RampIncrement,
		ReplyRate:     p.ReplyRate,
		StartedAt:     p.StartedAt,
		HealthState:   p.HealthState,
		HealthReason:  p.HealthReason,
		PausedUntil:   p.PausedUntil,
		CreatedAt:     p.CreatedAt,
		UpdatedAt:     p.UpdatedAt,
	}
}

// DayStat is one day's warmup counters for a mailbox, used by the detail series.
type DayStat struct {
	Day      pgtype.Date
	Sent     int32
	Received int32
	Inbox    int32
	Spam     int32
	Replies  int32
}

func dayStatFromGen(s gen.WarmupDailyStat) DayStat {
	return DayStat{
		Day:      s.Day,
		Sent:     s.Sent,
		Received: s.Received,
		Inbox:    s.Inbox,
		Spam:     s.Spam,
		Replies:  s.Replies,
	}
}

// UpsertParams carries the ramp settings for enabling or updating a
// participant. It is a domain-owned struct (not the generated params type) so
// the Store interface stays free of gen references, keeping the seam minimal.
type UpsertParams struct {
	MailboxID     uuid.UUID
	WorkspaceID   uuid.UUID
	StartVolume   int32
	MaxVolume     int32
	RampIncrement int32
	ReplyRate     float32
}

// OverviewRow is one participant enriched for the workspace overview: its ramp
// and health fields, the mailbox email, the trailing-7-day SENDER placement sums
// (inbox/spam), and today's sent count — all resolved in one workspace-pinned
// read. Inbox7d/Spam7d are the numerator/denominator inputs the service turns
// into inbox_rate_7d / spam_rate_7d; the denominator is Inbox7d+Spam7d (observed
// placements of this mailbox's SENT warmup mail), not received volume.
type OverviewRow struct {
	MailboxID     uuid.UUID
	Enabled       bool
	StartVolume   int32
	MaxVolume     int32
	RampIncrement int32
	ReplyRate     float32
	StartedAt     pgtype.Timestamptz
	HealthState   string
	HealthReason  string
	Lane          string
	LaneReason    string
	Email         string
	Inbox7d       int64
	Spam7d        int64
	TodaySent     int32
}

func overviewRowFromGen(r gen.ListWarmupOverviewRowsRow) OverviewRow {
	return OverviewRow{
		MailboxID:     r.MailboxID,
		Enabled:       r.Enabled,
		StartVolume:   r.StartVolume,
		MaxVolume:     r.MaxVolume,
		RampIncrement: r.RampIncrement,
		ReplyRate:     r.ReplyRate,
		StartedAt:     r.StartedAt,
		HealthState:   r.HealthState,
		HealthReason:  r.HealthReason,
		Lane:          r.Lane,
		LaneReason:    r.LaneReason,
		Email:         r.Email,
		Inbox7d:       r.Inbox7d,
		Spam7d:        r.Spam7d,
		TodaySent:     r.TodaySent,
	}
}

// Transition is one persisted row of the append-only decision record
// (warmup_state_transitions). The lane fields are pointers because rows written
// before pool lanes existed genuinely had no lane: the migration deliberately
// left them NULL rather than fabricating 'probation' in an audit trail.
// BouncePopulation is a pointer for the same reason — a row written before the
// campaign/warmup bounce split does not know which arm its samples counted.
type Transition struct {
	ID               uuid.UUID
	CreatedAt        pgtype.Timestamptz
	FromState        string
	ToState          string
	ReasonCode       string
	Reason           string
	FromLane         *string
	ToLane           *string
	LaneReasonCode   *string
	LaneReason       *string
	PlacementSamples int32
	SpamRate         float32
	BouncePopulation *string
	BounceSamples    int32
	BounceRate       float32
	ComplaintSamples int32
	ComplaintRate    float32
	InvalidTokens    int32
	PolicyVersion    string
}

func transitionFromGen(r gen.ListWarmupTransitionsRow) Transition {
	return Transition{
		ID:               r.ID,
		CreatedAt:        r.CreatedAt,
		FromState:        r.FromState,
		ToState:          r.ToState,
		ReasonCode:       r.ReasonCode,
		Reason:           r.Reason,
		FromLane:         r.FromLane,
		ToLane:           r.ToLane,
		LaneReasonCode:   r.LaneReasonCode,
		LaneReason:       r.LaneReason,
		PlacementSamples: r.PlacementSamples,
		SpamRate:         r.SpamRate,
		BouncePopulation: r.BouncePopulation,
		BounceSamples:    r.BounceSamples,
		BounceRate:       r.BounceRate,
		ComplaintSamples: r.ComplaintSamples,
		ComplaintRate:    r.ComplaintRate,
		InvalidTokens:    r.InvalidTokens,
		PolicyVersion:    r.PolicyVersion,
	}
}

// ---------------------------------------------------------------------------
// API response DTOs (spec §10 / api/openapi.yaml). These carry the snake_case
// JSON contract the frontend consumes. They are DISTINCT from the persistence
// view types above (Participant/DayStat/OverviewRow): the service maps domain
// types into these so the wire shape and the storage shape stay decoupled.
// ---------------------------------------------------------------------------

// WarmupParticipantDTO is the WarmupParticipant schema: a mailbox's enrollment
// state plus its computed today_sent / today_target.
type WarmupParticipantDTO struct {
	MailboxID     string  `json:"mailbox_id"`
	Enabled       bool    `json:"enabled"`
	StartVolume   int32   `json:"start_volume"`
	MaxVolume     int32   `json:"max_volume"`
	RampIncrement int32   `json:"ramp_increment"`
	ReplyRate     float32 `json:"reply_rate"`
	HealthState   string  `json:"health_state"`
	HealthReason  string  `json:"health_reason"`
	StartedAt     string  `json:"started_at"`
	TodaySent     int32   `json:"today_sent"`
	TodayTarget   int32   `json:"today_target"`
}

// WarmupMailboxDTO is the WarmupMailbox schema: a participant enriched with the
// mailbox email and rolling 7-day placement rates for the overview.
type WarmupMailboxDTO struct {
	MailboxID    string `json:"mailbox_id"`
	Email        string `json:"email"`
	Enabled      bool   `json:"enabled"`
	HealthState  string `json:"health_state"`
	HealthReason string `json:"health_reason"`
	// The POOL ELIGIBILITY axis. Required by the schema since lanes shipped, but
	// unpopulated until now, so the SPA saw undefined and rendered every mailbox as
	// "probation" regardless of its real lane.
	Lane              string   `json:"lane"`
	LaneReason        string   `json:"lane_reason"`
	TodaySent         int32    `json:"today_sent"`
	TodayTarget       int32    `json:"today_target"`
	PlacementSample7d int64    `json:"placement_sample_7d"`
	InboxRate7d       *float64 `json:"inbox_rate_7d"`
	SpamRate7d        *float64 `json:"spam_rate_7d"`
}

// WarmupOverviewDTO is the WarmupOverview schema: the pool summary plus per
// mailbox health/placement. active is true when pool_size >= 2.
type WarmupOverviewDTO struct {
	PoolSize  int                `json:"pool_size"`
	Active    bool               `json:"active"`
	Mailboxes []WarmupMailboxDTO `json:"mailboxes"`
}

// WarmupDayStatDTO is the WarmupDayStat schema: one UTC day of counters.
type WarmupDayStatDTO struct {
	Day      string `json:"day"`
	Sent     int32  `json:"sent"`
	Received int32  `json:"received"`
	Inbox    int32  `json:"inbox"`
	Spam     int32  `json:"spam"`
	Replies  int32  `json:"replies"`
}

// WarmupDetailDTO is the WarmupDetail schema: one participant plus its daily
// series (oldest first, up to 30 days).
type WarmupDetailDTO struct {
	Participant WarmupParticipantDTO `json:"participant"`
	Series      []WarmupDayStatDTO   `json:"series"`
}

// WarmupTransitionDTO is the WarmupTransition schema: one automated state change
// and the evidence behind it.
//
// The four lane fields are nullable in the contract and stay pointers here, so a
// pre-lane row serializes as null rather than as an empty string a UI would have
// to guess about. The rates are CONFIDENCE-ADJUSTED lower bounds, not observed
// fractions — the schema says so, because rendering them as raw percentages would
// misreport every thin sample.
//
// bounce_population is nullable on the same grounds and matters for the same
// reason: bounce_samples/bounce_rate describe ONE population (campaign or warmup
// hard bounces, never both — pooling them is the dilution defect Phase 1 removed),
// so a client rendering the pair without the label would report a campaign figure
// as a warmup one.
type WarmupTransitionDTO struct {
	ID               string  `json:"id"`
	CreatedAt        string  `json:"created_at"`
	FromState        string  `json:"from_state"`
	ToState          string  `json:"to_state"`
	ReasonCode       string  `json:"reason_code"`
	Reason           string  `json:"reason"`
	FromLane         *string `json:"from_lane"`
	ToLane           *string `json:"to_lane"`
	LaneReasonCode   *string `json:"lane_reason_code"`
	LaneReason       *string `json:"lane_reason"`
	PlacementSamples int32   `json:"placement_samples"`
	SpamRate         float64 `json:"spam_rate"`
	BouncePopulation *string `json:"bounce_population"`
	BounceSamples    int32   `json:"bounce_samples"`
	BounceRate       float64 `json:"bounce_rate"`
	ComplaintSamples int32   `json:"complaint_samples"`
	ComplaintRate    float64 `json:"complaint_rate"`
	InvalidTokens    int32   `json:"invalid_tokens"`
	PolicyVersion    string  `json:"policy_version"`
}

// WarmupTransitionPageDTO is the WarmupTransitionPage schema. transitions is
// never null: an empty history is [], because a client distinguishing "no rows"
// from "field missing" is a distinction with no meaning here.
type WarmupTransitionPageDTO struct {
	Transitions []WarmupTransitionDTO `json:"transitions"`
}
