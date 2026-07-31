// Package rotation picks which mailbox in a campaign's sender pool a contact is
// assigned to. Like platform/cadence it is pure: no database, no clock, no
// math/rand. Selection is a function of the candidate state handed in, so every
// mode is table-testable and two callers looking at the same pool state agree.
//
// Rotation spreads CONTACTS across the pool, not individual sends. The pool is
// resolved once per enrollment, at its first send, and every later step reuses
// that mailbox — a follow-up step is a reply in the same thread, so switching
// mailbox mid-thread would reference a Message-ID the new mailbox never sent.
package rotation

import (
	"cmp"
	"errors"
	"math"
	"slices"
	"time"
)

// ErrNoEligibleSender means no candidate can send right now — every pool member
// is disabled, inactive, or already at today's cap. The caller defers the send
// exactly as a capped single-mailbox send defers today; it is not a pool error.
var ErrNoEligibleSender = errors.New("rotation: no eligible sender in the pool")

// Mode values match the campaigns.rotation_mode CHECK constraint, so a stored
// mode needs no translation on the way in.
const (
	ModeRoundRobin = "round_robin"
	ModeLRU        = "least_recently_used"
	ModeWeighted   = "weighted"
)

// Candidate is one pool member with the state selection needs. RemainingToday is
// the mailbox's cold-sending cap for today — ramped AND already scaled by warmup
// health (platform/sendcap) — minus what it has already sent today.
// LastAssignedAt is the zero time for a mailbox that has never been assigned.
//
// Deliberately health-free: health arrives here folded into RemainingToday, so a
// health field or factor in this package would count it a SECOND time (a 'watch'
// mailbox penalised 0.7 on capacity and 0.7 again on score = 0.49). See
// TestCandidateCarriesNoHealthSignal.
type Candidate struct {
	MailboxID      string
	Weight         int
	RemainingToday int
	WarmupAgeDays  int
	AssignedCount  int64
	LastAssignedAt time.Time
}

// Select picks the mailbox for one enrollment. Candidates must already be
// filtered to the eligible set — this function ranks, it does not gate. Ties
// break on MailboxID, so the result is deterministic and a retry that re-reads
// the same pool state picks the same mailbox. Returns ErrNoEligibleSender when
// the set is empty.
func Select(mode string, candidates []Candidate) (Candidate, error) {
	if len(candidates) == 0 {
		return Candidate{}, ErrNoEligibleSender
	}
	return slices.MinFunc(candidates, order(mode)), nil
}

// order returns the mode's comparison, sorted best-first so slices.MinFunc
// yields the winner. Every comparison falls through to MailboxID, which is what
// makes tied candidates resolve deterministically instead of by slice order.
func order(mode string) func(a, b Candidate) int {
	switch mode {
	case ModeRoundRobin:
		// Fewest contacts assigned so far wins, which evens out the counts.
		return func(a, b Candidate) int {
			return cmp.Or(cmp.Compare(a.AssignedCount, b.AssignedCount), cmp.Compare(a.MailboxID, b.MailboxID))
		}
	case ModeLRU:
		// Oldest assignment wins; a never-assigned mailbox carries the zero time
		// and therefore sorts first without a special case.
		return func(a, b Candidate) int {
			return cmp.Or(a.LastAssignedAt.Compare(b.LastAssignedAt), cmp.Compare(a.MailboxID, b.MailboxID))
		}
	default:
		// ModeWeighted, and any unrecognized mode: 'weighted' is the column
		// default, so an unknown value resolves to the same behaviour the DB
		// would have given rather than failing the send.
		return func(a, b Candidate) int {
			// Highest score wins, so the score comparison is inverted for MinFunc.
			return cmp.Or(-cmp.Compare(score(a), score(b)), cmp.Compare(a.MailboxID, b.MailboxID))
		}
	}
}

// score ranks a candidate for ModeWeighted: the operator's weight, scaled by how
// much capacity it has left today, and by a log2 age term so an older mailbox
// absorbs more volume than a freshly-connected one. Capacity is a factor here
// rather than a filter — an eligible candidate always has RemainingToday > 0 —
// which is what makes 'weighted' the only mode that routes around a
// nearly-exhausted mailbox before it hits its cap.
//
// Warmup health is NOT a term: it already scaled the caller's RemainingToday
// (health gating), so multiplying by it again would square the penalty. This is
// pure capacity/weight/age arithmetic and must stay that way.
func score(c Candidate) float64 {
	return float64(max(c.Weight, 0)) *
		float64(max(c.RemainingToday, 0)) *
		(1 + math.Log2(float64(max(c.WarmupAgeDays, 0))+1))
}
