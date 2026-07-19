// Package campaign manages campaign CRUD, ownership validation against
// mailboxes/lists, and (in a later task) launching sends.
package campaign

// CampaignStatus is the typed enum mirrored by the DB CHECK constraint on
// campaigns.status.
type CampaignStatus string

const (
	StatusDraft   CampaignStatus = "draft"
	StatusRunning CampaignStatus = "running"
	StatusPaused  CampaignStatus = "paused"
	StatusDone    CampaignStatus = "done"
)
