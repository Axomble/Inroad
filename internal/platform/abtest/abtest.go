// Package abtest picks which variant of a sequence step a given enrollment
// receives. Pure functions, no data access, no randomness — see Select.
package abtest

import (
	"crypto/sha256"
	"encoding/binary"
	"slices"
)

// Variant is one candidate copy. ID is empty for the step's own base content
// (the model in migration 000053: a step IS variant A, and this table holds the
// alternatives), which is what makes an empty ID mean "store NULL in
// sends.variant_id" rather than "no variant was chosen".
type Variant struct {
	ID     string
	Weight int
}

// Select returns the variant that enrollmentID receives for stepID, or false
// when nothing is eligible.
//
// DETERMINISTIC, not random. The same (enrollment, step) always yields the same
// variant, because a send can be retried: the advance task is re-enqueued on a
// transient failure, and a crashed worker's stale claim is re-won by another.
// Re-rolling on each attempt would let one logical message go out as variant A
// on the first attempt and B on the retry — two different emails, one of which
// is a duplicate to the prospect, and both attributed to whichever roll landed
// last. It also means no assignment table: there is nothing to write, nothing to
// keep consistent with the send, and nothing to clean up.
//
// Weighting is by cumulative interval over a uniform hash of the pair, so a
// 3:1 split lands ~75/25 across a population while staying fixed per contact.
// Zero-weight variants are eligible for nothing, which is how a losing variant
// is retired without deleting the sends attributed to it.
//
// Order matters to the outcome and is the CALLER's responsibility: the same
// candidate list must be passed in the same order every time, or a retry could
// land in a different interval. Callers order by (base first, then variant id),
// which is stable regardless of how rows come back from the database.
func Select(enrollmentID, stepID string, candidates []Variant) (Variant, bool) {
	total := 0
	for _, c := range candidates {
		if c.Weight > 0 {
			total += c.Weight
		}
	}
	if total == 0 {
		return Variant{}, false
	}

	// The step id is part of the key so a contact is not pinned to "always the
	// first variant" across every step of the sequence: with the enrollment id
	// alone, a contact that drew A at step 1 would draw A at every step, and a
	// two-step campaign would compare two disjoint populations rather than two
	// copies.
	point := int(hashToRange(enrollmentID+"\x00"+stepID, uint64(total)))

	cumulative := 0
	for _, c := range candidates {
		if c.Weight <= 0 {
			continue
		}
		cumulative += c.Weight
		if point < cumulative {
			return c, true
		}
	}
	// Unreachable: point < total == cumulative after the final eligible
	// candidate. Returning the last eligible one rather than panicking keeps a
	// hypothetical arithmetic slip from stopping a send.
	for _, c := range slices.Backward(candidates) {
		if c.Weight > 0 {
			return c, true
		}
	}
	return Variant{}, false
}

// hashToRange maps key uniformly onto [0, n). SHA-256 is not chosen for
// security here but for distribution: a weak hash would correlate consecutive
// uuids and skew the split, and this runs once per send, not in a loop.
//
// The modulo introduces the usual bias, bounded by n/2^64 — with n at most a
// handful of variants, that is far below any effect an A/B test could resolve.
func hashToRange(key string, n uint64) uint64 {
	sum := sha256.Sum256([]byte(key))
	return binary.BigEndian.Uint64(sum[:8]) % n
}
