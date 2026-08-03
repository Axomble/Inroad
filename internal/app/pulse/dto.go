package pulse

// The wire contract for GET /pulse. FROZEN — the frontend's pulse card, nav
// counts, and overview tiles are built against these exact field names; a
// rename here is a breaking API change, not a refactor.

// Pulse is the whole payload: O(1) size regardless of workspace scale.
type Pulse struct {
	Mailboxes MailboxCounts  `json:"mailboxes"`
	Warmup    WarmupCounts   `json:"warmup"`
	Campaigns CampaignCounts `json:"campaigns"`
	Contacts  ContactCounts  `json:"contacts"`
	Sending   SendingStatus  `json:"sending"`
	Inbox     InboxCounts    `json:"inbox"`
	Attention []Attention    `json:"attention"`
}

type MailboxCounts struct {
	Total  int64 `json:"total"`
	Active int64 `json:"active"`
	Paused int64 `json:"paused"`
	Error  int64 `json:"error"`
}

// WarmupCounts buckets the ENABLED warmup participants by live health.
// AtRisk folds 'throttled' and 'paused' — both mean the engine is actively
// holding volume back.
type WarmupCounts struct {
	Pool    int64 `json:"pool"`
	Healthy int64 `json:"healthy"`
	Watch   int64 `json:"watch"`
	AtRisk  int64 `json:"at_risk"`
}

type CampaignCounts struct {
	Total   int64 `json:"total"`
	Running int64 `json:"running"`
	Draft   int64 `json:"draft"`
	Paused  int64 `json:"paused"`
}

type ContactCounts struct {
	Total int64 `json:"total"`
}

// SendingStatus is today's cold-send meter: what went out so far (UTC day,
// same boundary the caps enforce) against the workspace's health-scaled
// capacity for the day.
type SendingStatus struct {
	SentToday int64 `json:"sent_today"`
	DailyCap  int64 `json:"daily_cap"`
}

// InboxCounts is reserved for the inbox read-model; both fields are literal
// zeros until it ships (the contract carries them now so the frontend does
// not change shape later).
type InboxCounts struct {
	Unread     int64 `json:"unread"`
	Interested int64 `json:"interested"`
}

// Attention is one server-defined "needs attention" row. The card renders
// whatever appears here (worst-first), so new backend features add rows with
// zero frontend changes. Every row carries a truthful reason and a
// destination — a count with no explanation is a spec violation.
type Attention struct {
	Kind     string `json:"kind"`
	Severity string `json:"severity"`
	Count    int64  `json:"count"`
	Reason   string `json:"reason"`
	Href     string `json:"href"`
}
