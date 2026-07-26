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
		Email:         r.Email,
		Inbox7d:       r.Inbox7d,
		Spam7d:        r.Spam7d,
		TodaySent:     r.TodaySent,
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
	MailboxID    string  `json:"mailbox_id"`
	Email        string  `json:"email"`
	Enabled      bool    `json:"enabled"`
	HealthState  string  `json:"health_state"`
	HealthReason string  `json:"health_reason"`
	TodaySent    int32   `json:"today_sent"`
	TodayTarget  int32   `json:"today_target"`
	InboxRate7d  float64 `json:"inbox_rate_7d"`
	SpamRate7d   float64 `json:"spam_rate_7d"`
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
