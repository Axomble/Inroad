package coordinator

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"time"

	"github.com/inroad/inroad/internal/platform/warmup"
)

// issueLease mints the authority for one send to one partner.
//
// The terms are warmup's own lease, built by warmup.IssueLease, so the expiry
// arithmetic and the policy-version stamp keep the single home they already have
// and a coordinator-issued lease is validated at claim time by the unchanged
// warmup.LeaseValid.
func issueLease(req PairRequest, partnerID string) Lease {
	terms := warmup.IssueLease(req.Requester.Lane, req.Now)
	return Lease{ID: leaseID(req.Requester, partnerID, terms), Terms: terms}
}

// leaseID derives the lease identifier from everything the lease binds.
//
// Derived rather than random for two reasons. A retried warmup tick — the normal
// case on an asynq queue — recomputes the id it already had, so the retry cannot
// present itself as a second authority over the same pair. And every fact the
// lease asserts is inside the digest, so an id cannot be moved onto a different
// sender, partner, lane or window and still match what it claims to authorize.
//
// It is NOT a signature and proves nothing to a third party: it is unkeyed, and
// anyone holding the inputs can recompute it. Local verification does not need one
// (warmup.LeaseValid re-reads the sender's CURRENT lane, which is strictly better
// than a signature over a lane that may since have changed). A remote coordinator
// does need one, and that is a keyed field this package does not yet have — see
// the spec rather than filling this in with something unverified.
func leaseID(requester Participant, partnerID string, terms warmup.Lease) string {
	h := sha256.New()
	for _, field := range []string{
		requester.WorkspaceID,
		requester.ID,
		partnerID,
		terms.IssuedLane,
		terms.IssuedPolicyVersion,
		terms.ExpiresAt.UTC().Format(time.RFC3339Nano),
	} {
		// Length-prefixed, because plain concatenation lets the boundary between
		// two fields move without changing the bytes: requester "ab" with
		// partner "c" would hash the same as "a" with "bc".
		h.Write([]byte(strconv.Itoa(len(field))))
		h.Write([]byte(":"))
		h.Write([]byte(field))
	}
	return hex.EncodeToString(h.Sum(nil))
}
