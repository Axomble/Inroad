package agenttool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
)

// maxWarmupSeries bounds how much of a mailbox's daily history one call
// returns. The domain keeps 30 days; the most recent two weeks is what a
// trend question needs, and the rest is context a model pays for and ignores.
const maxWarmupSeries = 14

// WarmupMailbox is one mailbox's warmup state in the pool overview.
type WarmupMailbox struct {
	MailboxID    uuid.UUID
	Email        string
	Enabled      bool
	HealthState  string
	HealthReason string
	TodaySent    int32
	TodayTarget  int32
	InboxRate7d  float64
	SpamRate7d   float64
}

// WarmupOverview is the pool summary. Active is false for a pool too small to
// warm anything up, which is the usual reason warmup "isn't doing anything".
type WarmupOverview struct {
	PoolSize  int
	Active    bool
	Mailboxes []WarmupMailbox
}

// WarmupParticipant is one mailbox's ramp configuration and live state.
type WarmupParticipant struct {
	MailboxID     uuid.UUID
	Enabled       bool
	StartVolume   int32
	MaxVolume     int32
	RampIncrement int32
	ReplyRate     float32
	HealthState   string
	HealthReason  string
	StartedAt     string
	TodaySent     int32
	TodayTarget   int32
}

// WarmupDay is one UTC day of warmup counters.
type WarmupDay struct {
	Day      string
	Sent     int32
	Received int32
	Inbox    int32
	Spam     int32
	Replies  int32
}

// WarmupDetail is one participant plus its daily series, oldest first.
type WarmupDetail struct {
	Participant WarmupParticipant
	Series      []WarmupDay
}

// WarmupReader is what the warmup tool needs.
type WarmupReader interface {
	Overview(ctx context.Context, ws uuid.UUID) (WarmupOverview, error)
	Detail(ctx context.Context, ws, mailboxID uuid.UUID) (WarmupDetail, error)
}

func warmupTools(deps Deps) []Tool {
	if deps.Warmup == nil {
		return nil
	}
	return []Tool{warmupReadTool(deps.Warmup)}
}

type warmupReadArgs struct {
	baseArgs
	Method    string `json:"method"`
	MailboxID string `json:"mailbox_id"`
	Limit     *int   `json:"limit"`
}

func warmupReadTool(r WarmupReader) Tool {
	methods := []string{methodOverview, methodGet}
	return Tool{
		Name: "inroad_warmup_read",
		Description: "Read mailbox warmup: how each mailbox is ramping and where its warmup mail is landing. " +
			"Use method=overview for the pool — per-mailbox health, today's sent against today's target, and the rolling 7-day inbox and spam placement rates. " +
			"Use method=get with a mailbox_id for that mailbox's ramp settings plus its recent daily counters " +
			fmt.Sprintf("(the most recent %d days, oldest first). ", maxWarmupSeries) +
			"A pool that reports active=false is too small to warm anything up — that alone explains zero warmup volume.",
		InputSchema: mustSchema(
			enumField("method", "overview reads the whole pool; get reads one mailbox in detail.", methods, true),
			strField("mailbox_id", "The mailbox's id, from inroad_mailbox_read. Required for get.", false),
			limitField(),
		),
		Risk: RiskRead,
		Execute: func(ctx context.Context, p Principal, args json.RawMessage) (Result, error) {
			var a warmupReadArgs
			if bad := decodeArgs(args, &a); bad != nil {
				return *bad, nil
			}
			limit, bad := resolveLimit(a.Limit)
			if bad != nil {
				return *bad, nil
			}

			switch a.Method {
			case methodOverview:
				o, err := r.Overview(ctx, p.WorkspaceID)
				if err != nil {
					return Result{}, fmt.Errorf("warmup overview: %w", err)
				}
				boxes := o.Mailboxes
				if len(boxes) > limit {
					boxes = boxes[:limit]
				}
				items := make([]map[string]any, 0, len(boxes))
				for _, m := range boxes {
					items = append(items, map[string]any{
						"mailbox_id": m.MailboxID.String(), "email": m.Email, "enabled": m.Enabled,
						"health_state": m.HealthState, "health_reason": m.HealthReason,
						"today_sent": m.TodaySent, "today_target": m.TodayTarget,
						"inbox_rate_7d_pct": m.InboxRate7d, "spam_rate_7d_pct": m.SpamRate7d,
					})
				}
				return Ok(map[string]any{
					"pool_size": o.PoolSize, "active": o.Active,
					"mailboxes": items, "returned": len(items),
				}), nil
			case methodGet:
				id, bad := parseID("mailbox_id", a.MailboxID)
				if bad != nil {
					return *bad, nil
				}
				d, err := r.Detail(ctx, p.WorkspaceID, id)
				if err != nil {
					if isNoRecord(err) {
						return mailboxMissing(a.MailboxID), nil
					}
					return Result{}, fmt.Errorf("warmup detail: %w", err)
				}
				return Ok(renderWarmupDetail(d)), nil
			default:
				return unknownMethod(a.Method, methods), nil
			}
		},
	}
}

func renderWarmupDetail(d WarmupDetail) map[string]any {
	series := d.Series
	if len(series) > maxWarmupSeries {
		// Oldest first, so the tail is the recent history worth reporting.
		series = series[len(series)-maxWarmupSeries:]
	}
	days := make([]map[string]any, 0, len(series))
	for _, s := range series {
		days = append(days, map[string]any{
			"day": s.Day, "sent": s.Sent, "received": s.Received,
			"inbox": s.Inbox, "spam": s.Spam, "replies": s.Replies,
		})
	}
	p := d.Participant
	return map[string]any{
		"mailbox_id": p.MailboxID.String(), "enabled": p.Enabled,
		"start_volume": p.StartVolume, "max_volume": p.MaxVolume, "ramp_increment": p.RampIncrement,
		"reply_rate": p.ReplyRate, "health_state": p.HealthState, "health_reason": p.HealthReason,
		"started_at": p.StartedAt, "today_sent": p.TodaySent, "today_target": p.TodayTarget,
		"series": days,
	}
}
