package reporting

import "time"

// The wire contract for GET /reports/campaigns.

// Report is the whole payload: every campaign's lifetime performance plus the
// workspace roll-up.
type Report struct {
	Campaigns []CampaignPerformance `json:"campaigns"`
	Totals    counts                `json:"totals"`
}

// CampaignPerformance is one campaign's row in the comparison.
type CampaignPerformance struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	counts    `json:",inline"`
}

// counts carries the raw tallies and the rates derived from them.
//
// Unexported and embedded so the JSON stays flat (`sent`, `open_rate`, … at the
// row's top level) while the rate arithmetic has exactly one home — see
// withRates. Rates are fractions in [0,1], not percentages; the client formats
// them.
type counts struct {
	Sent         int64 `json:"sent"`
	Enrolled     int64 `json:"enrolled"`
	Opens        int64 `json:"opens"`
	Clicks       int64 `json:"clicks"`
	Replies      int64 `json:"replies"`
	Bounces      int64 `json:"bounces"`
	Unsubscribes int64 `json:"unsubscribes"`

	// Per send.
	OpenRate  float64 `json:"open_rate"`
	ClickRate float64 `json:"click_rate"`
	// Per enrolled contact.
	ReplyRate  float64 `json:"reply_rate"`
	BounceRate float64 `json:"bounce_rate"`
	UnsubRate  float64 `json:"unsub_rate"`
}
