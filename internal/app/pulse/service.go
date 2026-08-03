package pulse

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/sendcap"
)

// Attention severities, worst first. The service sorts rows by this rank so
// the top line of the sidebar is always the most important fact.
const (
	SeverityDanger = "danger"
	SeverityWarn   = "warn"
	SeverityInfo   = "info"
)

// Stable machine identifiers for the attention producers this domain ships.
// campaign_gated is deliberately absent: campaigns.status has no
// paused-by-system value (guardrails DEFER sends, they never flip a campaign
// to paused), so that producer cannot state a truthful condition yet.
const (
	kindMailboxError = "mailbox_error"
	kindSendersGated = "senders_gated"
	kindDmarcFailing = "dmarc_failing"
	kindCapConsumed  = "cap_consumed"
)

// capConsumedThresholdPct is the consumption level (percent of today's cap)
// at which the info-severity cap_consumed row appears.
const capConsumedThresholdPct = 90

// severityRank orders attention rows danger > warn > info. An unknown
// severity cannot occur (producers only use the constants above), but ranking
// it last keeps the sort total.
var severityRank = map[string]int{SeverityDanger: 0, SeverityWarn: 1, SeverityInfo: 2}

// Service assembles the pulse payload from the workspace-pinned aggregates.
// It depends on the consumer-defined Store, never the concrete PgStore. now
// is injectable purely so the ramp-age arithmetic is deterministic under
// test; production wires time.Now.
type Service struct {
	store Store
	now   func() time.Time
}

func NewService(store Store) *Service { return &Service{store: store, now: time.Now} }

// Get builds the whole pulse for one workspace. Attention is never nil: an
// all-clear workspace serializes as [], which is the payload the card's
// quiet/healthy state renders from.
func (s *Service) Get(ctx context.Context, workspaceID uuid.UUID) (Pulse, error) {
	mb, err := s.store.MailboxCounts(ctx, workspaceID)
	if err != nil {
		return Pulse{}, fmt.Errorf("pulse: mailbox counts: %w", err)
	}
	wu, err := s.store.WarmupCounts(ctx, workspaceID)
	if err != nil {
		return Pulse{}, fmt.Errorf("pulse: warmup counts: %w", err)
	}
	cam, err := s.store.CampaignCounts(ctx, workspaceID)
	if err != nil {
		return Pulse{}, fmt.Errorf("pulse: campaign counts: %w", err)
	}
	contacts, err := s.store.ContactCount(ctx, workspaceID)
	if err != nil {
		return Pulse{}, fmt.Errorf("pulse: contact count: %w", err)
	}
	sentToday, err := s.store.SentToday(ctx, workspaceID)
	if err != nil {
		return Pulse{}, fmt.Errorf("pulse: sent today: %w", err)
	}
	caps, err := s.store.SenderCapacities(ctx, workspaceID)
	if err != nil {
		return Pulse{}, fmt.Errorf("pulse: sender capacities: %w", err)
	}
	dmarc, err := s.store.DmarcAttention(ctx, workspaceID)
	if err != nil {
		return Pulse{}, fmt.Errorf("pulse: dmarc attention: %w", err)
	}

	capToday := s.capacityOf(caps)
	return Pulse{
		Mailboxes: MailboxCounts{Total: mb.Total, Active: mb.Active, Paused: mb.Paused, Error: mb.Error},
		Warmup:    WarmupCounts{Pool: wu.Pool, Healthy: wu.Healthy, Watch: wu.Watch, AtRisk: wu.AtRisk},
		Campaigns: CampaignCounts{Total: cam.Total, Running: cam.Running, Draft: cam.Draft, Paused: cam.Paused},
		Contacts:  ContactCounts{Total: contacts},
		Sending:   SendingStatus{SentToday: sentToday, DailyCap: capToday.dailyCap},
		Inbox:     InboxCounts{}, // literal zeros until the inbox read-model ships
		Attention: attention(mb, capToday, dmarc, sentToday),
	}, nil
}

// capacity is the workspace's cold-send picture for today, derived from the
// active mailboxes: the summed health-scaled cap, and how many senders warmup
// health is currently gating (per state, so the attention reason can say so).
type capacity struct {
	dailyCap  int64
	watch     int64
	throttled int64
	paused    int64
}

func (c capacity) gated() int64 { return c.watch + c.throttled + c.paused }

// capacityOf computes today's workspace cap with the SAME arithmetic the send
// path enforces (sendcap.Effective ramped cap, scaled by warmup health) — a
// second implementation of that math is a bug, so none exists here. A
// health-paused mailbox contributes 0: it cannot send cold mail right now,
// and the meter must not promise capacity the sender will not honour.
func (s *Service) capacityOf(rows []gen.ListPulseSenderCapacityRow) capacity {
	var c capacity
	// One instant for every row: cheaper than a clock read per mailbox, and the
	// whole meter is computed against the same "now".
	now := s.now()
	for _, r := range rows {
		ageDays := int(now.Sub(r.CreatedAt.Time).Hours() / 24)
		effective := sendcap.Effective(int(r.DailyCap), int(r.RampStartCap), int(r.RampDays), r.RampEnabled, ageDays)
		c.dailyCap += int64(sendcap.Cold(effective, r.HealthState))
		switch r.HealthState {
		case sendcap.HealthWatch:
			c.watch++
		case sendcap.HealthThrottled:
			c.throttled++
		case sendcap.HealthPaused:
			c.paused++
		}
	}
	return c
}

// attention runs the producers and returns their rows worst-first. Each
// producer only emits when it can state a truthful reason from real data.
func attention(mb gen.GetPulseMailboxCountsRow, capToday capacity, dmarc gen.GetPulseDmarcAttentionRow, sentToday int64) []Attention {
	rows := make([]Attention, 0, 4)
	if mb.Error > 0 {
		rows = append(rows, Attention{
			Kind: kindMailboxError, Severity: SeverityDanger, Count: mb.Error,
			Reason: mailboxErrorReason(mb.ErrorReason),
			Href:   "/app/mailboxes?status=error",
		})
	}
	if capToday.gated() > 0 {
		rows = append(rows, Attention{
			Kind: kindSendersGated, Severity: SeverityWarn, Count: capToday.gated(),
			Reason: gatedReason(capToday),
			Href:   "/app/mailboxes",
		})
	}
	if dmarc.Count > 0 {
		rows = append(rows, Attention{
			Kind: kindDmarcFailing, Severity: SeverityWarn, Count: dmarc.Count,
			Reason: dmarcReason(dmarc),
			// No dedicated deliverability page exists yet; mailboxes is the
			// closest surface that shows the sending identities involved.
			Href: "/app/mailboxes",
		})
	}
	if capToday.dailyCap > 0 && sentToday*100 >= capToday.dailyCap*capConsumedThresholdPct {
		rows = append(rows, Attention{
			Kind: kindCapConsumed, Severity: SeverityInfo, Count: 1,
			Reason: fmt.Sprintf("daily cap %d%% used", sentToday*100/capToday.dailyCap),
			Href:   "/app/campaigns",
		})
	}
	// Producers already append worst-first; the stable sort keeps that
	// guarantee true when a future producer lands out of order.
	sort.SliceStable(rows, func(i, j int) bool {
		return severityRank[rows[i].Severity] < severityRank[rows[j].Severity]
	})
	return rows
}

// mailboxErrorReason prefers the stored last_error sample; "in error state"
// is the truthful fallback when no erroring mailbox recorded a message (the
// status itself is real data).
func mailboxErrorReason(stored string) string {
	if stored != "" {
		return stored
	}
	return "mailbox in error state"
}

// gatedReason narrates WHICH health states are limiting sending, from the
// per-state counts — e.g. "warmup health limiting sending: 2 throttled, 1 paused".
func gatedReason(c capacity) string {
	parts := make([]string, 0, 3)
	if c.watch > 0 {
		parts = append(parts, fmt.Sprintf("%d on watch", c.watch))
	}
	if c.throttled > 0 {
		parts = append(parts, fmt.Sprintf("%d throttled", c.throttled))
	}
	if c.paused > 0 {
		parts = append(parts, fmt.Sprintf("%d paused", c.paused))
	}
	return "warmup health limiting sending: " + strings.Join(parts, ", ")
}

// dmarcReason names one offending domain (the query's deterministic sample)
// so the row is actionable, and counts the rest.
func dmarcReason(d gen.GetPulseDmarcAttentionRow) string {
	if d.Count == 1 {
		return fmt.Sprintf("%s has no verified DMARC record", d.SampleDomain)
	}
	return fmt.Sprintf("%s and %d more domains have no verified DMARC record", d.SampleDomain, d.Count-1)
}
