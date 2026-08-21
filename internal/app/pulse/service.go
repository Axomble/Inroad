package pulse

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/sendcap"
	"github.com/inroad/inroad/internal/platform/warmup"
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
	inbox, err := s.store.InboxCounts(ctx, workspaceID)
	if err != nil {
		return Pulse{}, fmt.Errorf("pulse: inbox counts: %w", err)
	}

	capToday := s.capacityOf(caps)
	return Pulse{
		Mailboxes: MailboxCounts{Total: mb.Total, Active: mb.Active, Paused: mb.Paused, Error: mb.Error},
		Warmup:    WarmupCounts{Pool: wu.Pool, Unknown: wu.Unknown, Healthy: wu.Healthy, Watch: wu.Watch, AtRisk: wu.AtRisk},
		Campaigns: CampaignCounts{Total: cam.Total, Running: cam.Running, Draft: cam.Draft, Paused: cam.Paused},
		Contacts:  ContactCounts{Total: contacts},
		Sending:   SendingStatus{SentToday: sentToday, DailyCap: capToday.dailyCap},
		Inbox:     InboxCounts{Unread: inbox.Unread, Interested: inbox.Interested},
		Attention: attention(attentionInputs{
			mailboxes: mb,
			capToday:  capToday,
			dmarc:     dmarc,
			sentToday: sentToday,
			incident:  s.strongestWarmupIncident(ctx, workspaceID, capToday),
		}),
	}, nil
}

// strongestWarmupIncident returns the correlation that best explains why warmup
// health is gating senders, or nil when there is nothing to explain or nothing shared
// to blame.
//
// IT RETURNS NO ERROR (design §9, last bullet). A failed inference must not fail the
// pulse: the row falls back to narrating its per-state counts, which is what it said
// before this slice and is still true. Logged, never swallowed silently.
//
// The read is SKIPPED unless a sender is actually gated, because there is no row to
// attribute otherwise. /pulse is polled by every open console session roughly every
// 45 seconds, and this is the only consumer.
//
// Note what gated() counts, because it is broader than "something is wrong": unknown
// is in it, and unknown is the default health state of every newly connected mailbox.
// So a workspace holding one unmeasured mailbox runs this read on every poll. That is
// the right coupling — the row exists for an unknown mailbox because an unknown
// mailbox really is being limited, and we only attribute a row that exists — but it
// is not free, and measuring it beat guessing: 100 participants at the 7-day ceiling
// of ~300 placement observations each is ~10ms, index-only on
// idx_warmup_observations_subject_time, all buffer hits.
//
// Only the STRONGEST finding is named. Two dimensions frequently describe the same
// fault from different angles — a signing domain and the sender domain it lives on —
// so counting "and 2 more" would inflate one problem into three on a card whose whole
// job is to be trusted. The warmup overview shows every finding with its arithmetic.
//
// That selection is also where an influenced dimension bites: two of the four are
// steerable by an actor with read/write on a single warmup recipient mailbox (see
// warmup.DetectIncidents), so a fabricated high-lift cohort can DISPLACE a true
// finding from this one line. Survivable rather than fine, and only because the
// overview this row links to lists every finding, capped and disclosed. Do not turn
// this line into the only place a correlation is reported.
func (s *Service) strongestWarmupIncident(ctx context.Context, workspaceID uuid.UUID, capToday capacity) *warmup.Incident {
	if capToday.gated() == 0 {
		return nil
	}
	participants, err := s.store.WarmupIncidentParticipants(ctx, workspaceID)
	if err != nil {
		slog.ErrorContext(ctx, "pulse_warmup_incident_detection_failed",
			"workspace_id", workspaceID, "err", err)
		return nil
	}
	found := warmup.DetectIncidents(participants)
	if len(found) == 0 {
		return nil
	}
	// DetectIncidents already sorts strongest-first and deterministically, so the head
	// is the finding to name. Re-ranking here would be a second opinion about which
	// correlation matters most.
	return &found[0]
}

// capacity is the workspace's cold-send picture for today, derived from the
// active mailboxes: the summed health-scaled cap, and how many senders warmup
// health is currently gating (per state, so the attention reason can say so).
type capacity struct {
	dailyCap  int64
	unknown   int64
	watch     int64
	throttled int64
	paused    int64
}

func (c capacity) gated() int64 { return c.unknown + c.watch + c.throttled + c.paused }

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
		case sendcap.HealthUnknown:
			c.unknown++
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

// attentionInputs are the facts the producers run over — one struct rather than a
// growing positional list, so adding a producer's input cannot silently reorder an
// existing one's.
type attentionInputs struct {
	mailboxes gen.GetPulseMailboxCountsRow
	capToday  capacity
	dmarc     gen.GetPulseDmarcAttentionRow
	sentToday int64
	// incident is the shared cause behind the gated senders, or nil when none was
	// found. It ATTRIBUTES the senders_gated row and never produces a row of its own:
	// a second row about the same mailboxes would double-count exactly the thing
	// correlating them exists to collapse (design §8).
	incident *warmup.Incident
}

// attention runs the producers and returns their rows worst-first. Each
// producer only emits when it can state a truthful reason from real data.
func attention(in attentionInputs) []Attention {
	mb, capToday, dmarc, sentToday := in.mailboxes, in.capToday, in.dmarc, in.sentToday
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
			// Kind, severity and count are UNCHANGED by attribution: this is the same row
			// saying the same thing about the same mailboxes, with a better explanation.
			Kind: kindSendersGated, Severity: SeverityWarn, Count: capToday.gated(),
			Reason: gatedReason(capToday, in.incident),
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

// gatedPrefix keeps the row recognisable across both of its forms: whichever tail
// follows, the fact being reported is still that warmup health is holding volume back.
const gatedPrefix = "warmup health limiting sending: "

// gatedReason explains why warmup health is limiting sending.
//
// With a detected incident it NAMES THE SHARED THING — "4 mailboxes degrading through
// one signing domain (mail.acme.test)" — instead of enumerating states. That is the
// whole behaviour change of this slice: an operator told "3 throttled, 1 paused" has
// to diff four mailboxes by hand to notice they all sign as one domain.
//
// Without one it falls back to the per-state counts, which is the honest answer: "no
// shared cause found" is information, and inventing an explanation from an
// unconcentrated pool would be worse than a count.
func gatedReason(c capacity, incident *warmup.Incident) string {
	if incident != nil {
		return gatedPrefix + incidentCause(*incident)
	}
	parts := make([]string, 0, 4)
	if c.unknown > 0 {
		parts = append(parts, fmt.Sprintf("%d need evidence", c.unknown))
	}
	if c.watch > 0 {
		parts = append(parts, fmt.Sprintf("%d on watch", c.watch))
	}
	if c.throttled > 0 {
		parts = append(parts, fmt.Sprintf("%d throttled", c.throttled))
	}
	if c.paused > 0 {
		parts = append(parts, fmt.Sprintf("%d paused", c.paused))
	}
	return gatedPrefix + strings.Join(parts, ", ")
}

// incidentCause is the attributed tail of the senders_gated reason.
//
// "degrading THROUGH one X" deliberately, not "because of": an incident is a
// correlation and never a cause (design §8). The value is named in full because a
// finding an operator cannot act on is not worth a row, and the member count is the
// incident's OWN — the degraded mailboxes inside the cohort, which is a different and
// separately-labelled population from the row's count of gated senders (that one
// includes mailboxes merely lacking evidence, and excludes lane-only containment).
func incidentCause(in warmup.Incident) string {
	label, ok := incidentDimensionLabels[in.Dimension]
	if !ok {
		// A dimension added to the fold and not to the labels reads a little
		// mechanically, and that is the correct failure: a sentence with a hole in it, or
		// worse a plausible wrong noun, is not a better outcome.
		label = in.Dimension
	}
	return fmt.Sprintf("%d mailboxes degrading through one %s (%s)", in.DegradedInside, label, in.Value)
}

// incidentDimensionLabels turn the wire vocabulary into the noun an operator reads.
// A dimension with no label here would render as an empty noun, so the fallback names
// the raw dimension instead — a new fault dimension must never produce a sentence
// with a hole in it.
var incidentDimensionLabels = map[string]string{
	warmup.DimensionRoute:        "destination route",
	warmup.DimensionSigning:      "signing domain",
	warmup.DimensionReturnPath:   "return path domain",
	warmup.DimensionSenderDomain: "sender domain",
}

// dmarcReason names one offending domain (the query's deterministic sample)
// so the row is actionable, and counts the rest.
func dmarcReason(d gen.GetPulseDmarcAttentionRow) string {
	if d.Count == 1 {
		return fmt.Sprintf("%s has no verified DMARC record", d.SampleDomain)
	}
	return fmt.Sprintf("%s and %d more domains have no verified DMARC record", d.SampleDomain, d.Count-1)
}
