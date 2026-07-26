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

// PlacementRate is one mailbox's trailing-7-day inbox/spam/received counts,
// from which the overview derives inbox_rate_7d / spam_rate_7d.
type PlacementRate struct {
	MailboxID uuid.UUID
	Inbox     int64
	Spam      int64
	Received  int64
}

func placementRateFromGen(r gen.GetWarmupPlacementRates7dRow) PlacementRate {
	return PlacementRate{
		MailboxID: r.MailboxID,
		Inbox:     r.Inbox,
		Spam:      r.Spam,
		Received:  r.Received,
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
