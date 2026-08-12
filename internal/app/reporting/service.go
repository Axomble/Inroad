package reporting

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// Service assembles the cross-campaign report. It depends on the
// consumer-defined Store, never the concrete PgStore.
type Service struct{ store Store }

func NewService(store Store) *Service { return &Service{store: store} }

// CampaignPerformance returns one row per campaign plus the workspace totals.
//
// Never nil: a workspace with no campaigns serializes as an empty list and a
// zeroed total, which is the payload the report's empty state renders from.
func (s *Service) CampaignPerformance(ctx context.Context, workspaceID uuid.UUID) (Report, error) {
	rows, err := s.store.CampaignPerformance(ctx, workspaceID)
	if err != nil {
		return Report{}, fmt.Errorf("reporting: campaign performance: %w", err)
	}

	out := Report{Campaigns: make([]CampaignPerformance, 0, len(rows))}
	var totals counts
	for _, r := range rows {
		out.Campaigns = append(out.Campaigns, CampaignPerformance{
			ID:        r.ID.String(),
			Name:      r.Name,
			Status:    r.Status,
			CreatedAt: r.CreatedAt.Time,
			counts: counts{
				Sent: r.Sent, Enrolled: r.Enrolled, Opens: r.Opens, Clicks: r.Clicks,
				Replies: r.Replies, Bounces: r.Bounces, Unsubscribes: r.Unsubscribes,
			}.withRates(),
		})
		totals = totals.add(r)
	}
	out.Totals = totals.withRates()
	return out, nil
}

// add accumulates one campaign's raw counts. The workspace total is summed from
// the counts and its rates computed ONCE at the end — averaging the per-campaign
// rates instead would weight a 10-send campaign equally with a 100,000-send one.
func (c counts) add(r gen.ListCampaignPerformanceRow) counts {
	return counts{
		Sent:         c.Sent + r.Sent,
		Enrolled:     c.Enrolled + r.Enrolled,
		Opens:        c.Opens + r.Opens,
		Clicks:       c.Clicks + r.Clicks,
		Replies:      c.Replies + r.Replies,
		Bounces:      c.Bounces + r.Bounces,
		Unsubscribes: c.Unsubscribes + r.Unsubscribes,
	}
}

// withRates fills the derived rates from the counts.
//
// The two denominators are NOT interchangeable and mirror
// campaign.computeMetrics exactly: opens and clicks are per SEND (one message
// opened), while replies, bounces and unsubscribes are per ENROLLED CONTACT
// (a person replied once, however many messages they got). The arithmetic is
// restated here rather than imported because app/* packages don't import each
// other — the same reason agenttool restates roleRank. campaign.computeMetrics
// remains the canonical definition; if it changes, this changes with it.
//
// Each denominator is guarded rather than divided by zero: a campaign that has
// sent nothing has no open rate, and 0/0 must read as 0, not NaN — NaN does not
// survive JSON encoding.
func (c counts) withRates() counts {
	if c.Sent > 0 {
		sent := float64(c.Sent)
		c.OpenRate = float64(c.Opens) / sent
		c.ClickRate = float64(c.Clicks) / sent
	}
	if c.Enrolled > 0 {
		enrolled := float64(c.Enrolled)
		c.ReplyRate = float64(c.Replies) / enrolled
		c.BounceRate = float64(c.Bounces) / enrolled
		c.UnsubRate = float64(c.Unsubscribes) / enrolled
	}
	return c
}
