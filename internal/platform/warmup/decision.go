package warmup

// ReplyDecision decides, deterministically, whether the next warmup send for a
// mailbox should be a reply into an existing thread (true) or open a fresh thread
// (false). It replaces math/rand with the package's seeded hash so a retried tick
// on the same seed reaches the SAME decision — the send path must be reproducible.
//
// seed is any stable per-decision string the caller controls (e.g. the sender
// mailbox + partner + the day's send index). replyRate is the participant's
// configured P(reply) in [0,1]; values outside the range clamp to never/always so
// a misconfigured rate can never index out of the bucket space.
func ReplyDecision(seed string, replyRate float64) bool {
	if replyRate <= 0 {
		return false
	}
	if replyRate >= 1 {
		return true
	}
	// 1e6 buckets of resolution: frac in [0,1); reply when it lands under the rate.
	frac := float64(hashU64("reply", seed)%1_000_000) / 1_000_000.0
	return frac < replyRate
}
