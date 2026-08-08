package contact

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// ErrNotFound is what the handler maps to 404 for a contact id that does not
// exist in the caller's workspace. A contact in ANOTHER workspace produces
// exactly this error, so the two cases are indistinguishable to a client.
var ErrNotFound = errors.New("contact not found")

// ErrCompanyNotFound is the 404 for a company id that is not in the caller's
// workspace. Kept distinct from ErrNotFound so the two 404s can say which record
// was missing: both are workspace-scoped, so naming your own missing record
// leaks nothing.
var ErrCompanyNotFound = errors.New("company not found")

// Record-page bounds. Neither list paginates: a record page renders a roster,
// and the cap is what stops a contact with a pathological number of rows from
// turning one page load into a scan. The caller asks the store for cap+1 rows
// and reports the surplus as a truncation flag, so "there are more" costs no
// extra query.
const (
	// DealCap bounds the deals embedded in a contact record.
	DealCap = 25
	// CampaignCap bounds the enrollments listed on the engagement rollup. The
	// rollup's COUNTS are exact regardless of this cap — they come from
	// aggregates, not from this list.
	CampaignCap = 20
)

// stop_reason values the engagement rollup reads. Duplicated as plain strings
// rather than importing internal/app/enrollment (app packages do not import
// each other) and deliberately identical to the set campaign.Metrics uses, so a
// contact's reply/bounce/unsubscribe counts roll up to the campaign's.
const (
	stopReasonReplied    = "replied"
	stopReasonBounced    = "bounced"
	stopReasonSuppressed = "suppressed"
)

// Suppression reason literals, as stored by the suppression table's own CHECK
// constraint (migrations 000003 and 000037). Duplicated as plain strings rather
// than importing internal/app/suppression, which app packages may not do; the
// CHECK is the source of truth these mirror.
//
// complaint is deliberately NOT collapsed into unsubscribe: "they asked to stop"
// and "they reported us as spam" are different facts about a relationship, and
// 000037 added the distinction precisely so it survives to a screen.
const (
	SuppressionUnsubscribe = "unsubscribe"
	SuppressionBounce      = "bounce"
	SuppressionComplaint   = "complaint"
	SuppressionManual      = "manual"
)

// RecordSuppression answers whether a contact may be emailed, and why not.
//
// IsPrimaryEmail separates the two situations this can describe. True means the
// suppressed address IS the one the send path resolves (contacts.email), so this
// person cannot be emailed at all. False means only a secondary alias is
// suppressed: sending works today, but promoting that alias to primary would
// silently stop it.
type RecordSuppression struct {
	Reason         string
	Email          string
	IsPrimaryEmail bool
	SuppressedAt   time.Time
}

// RecordCompany is the company a contact belongs to, reduced to what a record
// page links with. The free-text company an import row carried is not this.
type RecordCompany struct {
	ID     uuid.UUID
	Name   string
	Domain string
}

// RecordDeal is one deal the contact is the primary contact on, carrying the
// stage fields a stage chip renders.
type RecordDeal struct {
	ID           uuid.UUID
	Name         string
	PipelineID   uuid.UUID
	StageID      uuid.UUID
	StageLabel   string
	StageColor   string
	StageIsWon   bool
	StageIsLost  bool
	AmountMicros *int64
	Currency     string
	CloseDate    *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Record is one contact's page: its own fields plus the cheap relational ones.
// Engagement is deliberately NOT here — see Engagement.
type Record struct {
	ID          uuid.UUID
	Email       string
	FirstName   string
	LastName    string
	JobTitle    string
	LinkedInURL string
	// Suppression is nil when no address of this contact is suppressed. It leads
	// the record because "may I email this person" outranks every engagement
	// number on the page.
	Suppression *RecordSuppression
	Company     *RecordCompany
	Deals       []RecordDeal
	// DealCount is the contact's TRUE deal total, counted independently of the
	// capped Deals slice. It exists so a truncated list can still be rendered
	// honestly ("25 of 38") rather than passing the cap off as the whole set.
	DealCount      int64
	DealsTruncated bool
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// SendStats is the sends half of the engagement rollup.
//
// OpensMeasurable is computed over the contact's WHOLE send history, which is why
// it lives here rather than being inferred from the (capped) Campaigns list — see
// Engagement.OpensMeasurable.
type SendStats struct {
	EmailsSent      int64
	LastSentAt      *time.Time
	OpensMeasurable bool
}

// TrackingStats is the tracking-events half of the engagement rollup. Both
// counts are per-send (distinct sends with an event), matching the denominator
// the rates use.
type TrackingStats struct {
	OpensIndicative int64
	Clicks          int64
	LastEventAt     *time.Time
}

// CampaignEnrollment is one campaign the contact is (or was) enrolled in.
//
// TrackingEnabled is the only thing that tells a zero open count apart from an
// unmeasured one: a campaign with tracking off contributes sends but cannot
// contribute opens or clicks. The rollup's counts do not adjust for it (neither
// does campaign.Metrics, and the two must agree), so this is how a caller
// explains a zero rather than guessing at it.
type CampaignEnrollment struct {
	CampaignID      uuid.UUID
	CampaignName    string
	TrackingEnabled bool
	Status          string
	CurrentStep     int32
	StopReason      *string
	EnrolledAt      time.Time
	LastSentAt      *time.Time
}

// Engagement is the per-contact outreach rollup: the platform's send/open/click/
// reply history for one person, which is the thing a relational CRM structurally
// cannot show. Every count reuses the campaign rollup's definition (see
// internal/app/campaign.Metrics) so the two can never disagree.
//
// OpenRate/ClickRate divide by EmailsSent — per-send, because a multi-step
// sequence sends several times per contact — and are 0 when nothing has been
// sent. Replies/Bounces/Unsubscribes are counts only: their per-contact
// denominator is CampaignsEnrolled, which is not a rate anyone reads for one
// person.
type Engagement struct {
	ContactID         uuid.UUID
	EmailsSent        int64
	OpensIndicative   int64
	Clicks            int64
	Replies           int64
	Bounces           int64
	Unsubscribes      int64
	OpenRate          float64
	ClickRate         float64
	CampaignsEnrolled int64
	// OpensMeasurable reports whether an open COULD have been recorded for this
	// contact: true when at least one send that actually went out belonged to a
	// campaign with tracking on. False with EmailsSent > 0 means the zero opens
	// and clicks above are unmeasured, not observed.
	//
	// It is computed server-side over the whole history on purpose. A client can
	// only see Campaigns, which is capped at CampaignCap — so for a contact with
	// more enrollments than that, whose newest are untracked and whose older ones
	// were tracked, any client-side `some(tracking_enabled)` would answer false
	// and explain away a genuine zero. The cap makes that inference unsound
	// exactly for the heavily-enrolled contacts whose numbers matter most.
	OpensMeasurable    bool
	LastActivityAt     *time.Time
	Campaigns          []CampaignEnrollment
	CampaignsTruncated bool
}

// computeEngagement assembles the rollup from the four independent reads.
// stopReasons is keyed by stop_reason with "" for an enrollment that has not
// stopped, so summing its values yields the lifetime enrollment count and the
// total can never disagree with the per-reason counts.
func computeEngagement(contactID uuid.UUID, sends SendStats, tracking TrackingStats,
	stopReasons map[string]int64, campaigns []CampaignEnrollment, truncated bool) Engagement {
	e := Engagement{
		ContactID:          contactID,
		EmailsSent:         sends.EmailsSent,
		OpensIndicative:    tracking.OpensIndicative,
		Clicks:             tracking.Clicks,
		Replies:            stopReasons[stopReasonReplied],
		Bounces:            stopReasons[stopReasonBounced],
		Unsubscribes:       stopReasons[stopReasonSuppressed],
		CampaignsEnrolled:  sumCounts(stopReasons),
		OpensMeasurable:    sends.OpensMeasurable,
		LastActivityAt:     latest(sends.LastSentAt, tracking.LastEventAt),
		Campaigns:          campaigns,
		CampaignsTruncated: truncated,
	}
	if e.Campaigns == nil {
		e.Campaigns = []CampaignEnrollment{}
	}
	// Guarded rather than divided: a contact who has never been sent to reads 0,
	// not NaN.
	if e.EmailsSent > 0 {
		sent := float64(e.EmailsSent)
		e.OpenRate = float64(e.OpensIndicative) / sent
		e.ClickRate = float64(e.Clicks) / sent
	}
	return e
}

func sumCounts(counts map[string]int64) int64 {
	var total int64
	for _, n := range counts {
		total += n
	}
	return total
}

// latest returns whichever timestamp is more recent, or nil when neither
// happened.
func latest(a, b *time.Time) *time.Time {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	case b.After(*a):
		return b
	default:
		return a
	}
}
