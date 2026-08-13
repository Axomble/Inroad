package campaign

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/rotation"
)

// Sentinel errors for sender-pool validation, mapped to 422 by the handler.
// A mailbox that is missing from the workspace or not active reuses
// ErrMailboxNotActive — the same verdict Create gives for the same reason.
var (
	ErrEmptySenderPool = errors.New("sender pool must contain at least one mailbox")
	ErrDuplicateSender = errors.New("mailbox listed more than once in the sender pool")
	ErrSenderWeight    = errors.New("sender weight must be between 1 and 100")
	ErrRotationMode    = errors.New("unknown rotation mode")
)

// Weight bounds, mirroring the campaign_senders.weight CHECK constraint so a
// rejected weight is a 422 rather than a constraint violation surfacing as a 500.
const (
	minSenderWeight = 1
	maxSenderWeight = 100
)

// defaultSenderWeight is the weight a request that omits one gets — equal
// footing with every other unweighted member.
const defaultSenderWeight = 1

// mailboxStatusActive is the only mailbox status that takes cold volume. A plain
// string rather than an import: app/* packages don't depend on each other, and the
// mailbox domain owns the vocabulary.
const mailboxStatusActive = "active"

// Sender is one member of a campaign's sender pool: the mailbox identity the UI
// displays (read-only), the operator-owned weight/enabled flags, the rotation
// state so an operator can see the spread actually happening, and today's health
// and capacity so a campaign sending slower than configured explains itself
// instead of looking broken. LastAssignedAt is nil for a mailbox that has never
// been assigned a contact.
//
// HealthState is nil when the mailbox is not warming up (including a disabled
// warmup participant, whose stored state is frozen and therefore not a live
// signal). Sending is false when the mailbox is taking no cold volume at all —
// paused by warmup, held out of the rotation, or not active. CapToday is the cap
// the send path will actually enforce: ramped AND health-scaled, computed with the
// same platform/sendcap arithmetic, so the panel cannot promise capacity the
// sender does not honour.
type Sender struct {
	MailboxID      uuid.UUID
	Email          string
	Provider       string
	Status         string
	Weight         int
	Enabled        bool
	AssignedCount  int64
	LastAssignedAt *time.Time
	HealthState    *string
	// Lane is the pool-eligibility axis, independent of HealthState. A mailbox can
	// be reputation-healthy and still sit in probation, or sit in quarantine while
	// its last measured reputation looked fine. Empty when not warming up.
	Lane *string
	// DomainLane is the WORST lane among the mailboxes sharing this one's
	// organizational domain, itself included. New campaign leads are gated on
	// mailbox AND domain, because a quarantined mailbox has almost certainly
	// damaged the standing of every sibling sending from the same domain. Empty
	// when no mailbox on the domain is warming up.
	DomainLane *string
	Sending    bool
	CapToday   int
	SentToday  int
}

// SenderPool is a campaign's whole pool plus the mode that selects from it.
type SenderPool struct {
	RotationMode string
	Senders      []Sender
}

// SenderInput is one requested pool member on a full replace. Weight/Enabled are
// already defaulted by the handler, so the service validates concrete values.
type SenderInput struct {
	MailboxID uuid.UUID
	Weight    int
	Enabled   bool
}

// GetSenders returns the campaign's sender pool and rotation mode.
//
// A campaign with no pool rows was never configured (created before pools
// existed, or by a path that doesn't seed them) rather than broken: it still
// sends, from campaigns.mailbox_id. That implicit single-mailbox pool is what is
// reported, so the panel shows the mailbox the campaign will actually send from
// instead of an empty list that contradicts its behaviour.
func (s *Service) GetSenders(ctx context.Context, ws, campaignID uuid.UUID) (SenderPool, error) {
	c, err := s.store.Get(ctx, ws, campaignID)
	if err != nil {
		return SenderPool{}, ErrNotFound
	}
	senders, err := s.loadSenderPool(ctx, ws, campaignID)
	if err != nil {
		return SenderPool{}, err
	}
	return SenderPool{RotationMode: normalizeRotationMode(c.RotationMode), Senders: senders}, nil
}

// loadSenderPool returns the campaign's sender pool, falling back to the
// implicit one-mailbox pool (campaigns.mailbox_id) when no campaign_senders
// rows exist -- the "never configured, not broken" projection every reader of
// the pool shares (GetSenders' response, the preflight loader's sender_pool/
// daily_limit/warmup_health evidence, and test-send's eligible-sender pick).
// One implementation means all three can never disagree about which mailboxes
// a campaign actually sends from.
func (s *Service) loadSenderPool(ctx context.Context, ws, campaignID uuid.UUID) ([]Sender, error) {
	senders, err := s.store.ListSenders(ctx, ws, campaignID)
	if err != nil {
		return nil, err
	}
	if len(senders) > 0 {
		return senders, nil
	}
	fallback, err := s.store.FallbackSender(ctx, ws, campaignID)
	if err != nil {
		return nil, err
	}
	return []Sender{fallback}, nil
}

// SetSenders replaces the pool and rotation mode wholesale. Every check runs
// before any write, so a rejected pool leaves the previous one intact.
//
// Editable while the campaign is running, like SetSchedule: it affects future
// contact assignments only and never moves a thread already in flight — a
// follow-up step must send from the mailbox that started the thread. Rotation
// counters survive for mailboxes that stay in the pool (see ReplaceSenders), so
// editing a weight doesn't reset the spread.
func (s *Service) SetSenders(ctx context.Context, ws, campaignID uuid.UUID, mode string, in []SenderInput) (SenderPool, error) {
	if _, err := s.store.Get(ctx, ws, campaignID); err != nil {
		return SenderPool{}, ErrNotFound
	}
	if !knownRotationMode(mode) {
		return SenderPool{}, ErrRotationMode
	}
	if len(in) == 0 {
		return SenderPool{}, ErrEmptySenderPool
	}
	seen := make(map[uuid.UUID]struct{}, len(in))
	for _, sender := range in {
		if sender.Weight < minSenderWeight || sender.Weight > maxSenderWeight {
			return SenderPool{}, ErrSenderWeight
		}
		if _, dup := seen[sender.MailboxID]; dup {
			return SenderPool{}, ErrDuplicateSender
		}
		seen[sender.MailboxID] = struct{}{}
		// The same workspace-scoped check Create runs: a mailbox from another
		// workspace is indistinguishable from a missing one, and both are a 422.
		active, err := s.checker.MailboxActive(ctx, ws, sender.MailboxID)
		if err != nil {
			return SenderPool{}, err
		}
		if !active {
			return SenderPool{}, ErrMailboxNotActive
		}
	}
	if err := s.store.ReplaceSenders(ctx, ws, campaignID, mode, in); err != nil {
		return SenderPool{}, err
	}
	// Re-read rather than echo the request: the response carries the rotation
	// state and mailbox identity only the database knows.
	return s.GetSenders(ctx, ws, campaignID)
}

// knownRotationMode reports whether mode is one the rotation package (and the
// campaigns.rotation_mode CHECK constraint) accepts.
func knownRotationMode(mode string) bool {
	switch mode {
	case rotation.ModeRoundRobin, rotation.ModeLRU, rotation.ModeWeighted:
		return true
	default:
		return false
	}
}

// normalizeRotationMode keeps the read path inside the API's enum. A stored value
// outside it can only come from a direct write, and the column's default is what
// selection would apply anyway, so reporting 'weighted' beats failing the read.
func normalizeRotationMode(mode string) string {
	if knownRotationMode(mode) {
		return mode
	}
	return rotation.ModeWeighted
}
