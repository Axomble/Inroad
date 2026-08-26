package warmup

// Sentinels: controlled, high-trust measurement endpoints.
//
// Every lane today may exchange warmup mail only with its OWN lane
// (LanesCompatible). That isolation is correct and it has a cost the design names
// (§4): a mailbox on `watch` can only be measured against other degrading mailboxes,
// so the evidence that would clear it — or condemn it — comes from peers whose own
// standing is in question. There is no way to diagnose early degradation without
// either exposing healthy peers to it or measuring it against nothing trustworthy.
//
// A sentinel is the operator's answer to that: a mailbox they control end to end and
// are willing to expose to any lane, precisely so degrading mailboxes have something
// dependable to be measured against.
//
// SENTINEL IS NOT A LANE, although the design's table lists it as one. A sentinel has
// its own health state and its own lane like every other participant — it can
// degrade, be contained, and recover — and folding that into the lane column would
// make "sentinel" and "watch" mutually exclusive when a sentinel that starts
// degrading is exactly the case that must remain representable. One column with two
// meanings is the defect this subsystem has been corrected for more than once
// (`inbox` that also meant "primary", `bounce_rate` that also counted soft bounces).
// So it is an orthogonal marker, and the lane keeps its single meaning.

// Participant is the pair of facts a pairing decision needs. Small on purpose: the
// pairing rule must not grow into a second opinion about health.
type Participant struct {
	Lane       string
	IsSentinel bool
}

// Pairable reports whether these two may exchange warmup mail.
//
// Same-lane pairing is unchanged, so a pool with no sentinels behaves exactly as it
// did — which matters, because that is most self-hosted installations and the design
// (§4) requires the no-sentinel case to keep working rather than degrade.
//
// A sentinel widens it: it may pair with any lane that may send at all. That is the
// whole point — `watch` and `recovery` gain something dependable to be measured
// against, and healthy customer mailboxes still never receive traffic from a
// degrading member, because the sentinel absorbs that exposure instead.
//
// Both sides must still pass LaneMaySend. A sentinel does NOT re-admit a quarantined
// or blocked mailbox: containment outranks measurement, and a sentinel that could
// reach into quarantine would make the breaker negotiable. That check stays exactly
// where it was rather than being restated here.
func Pairable(sender, recipient Participant) bool {
	s, r := normalizeLane(sender.Lane), normalizeLane(recipient.Lane)
	if !LaneMaySend(s) || !LaneMaySend(r) {
		return false
	}
	if sender.IsSentinel || recipient.IsSentinel {
		return true
	}
	return s == r
}

// EvidenceConfidence is how much a body of placement evidence is worth, which
// depends on WHO produced it.
//
// The design (§4) is explicit that this must be labelled rather than assumed: "for
// self-hosted installations without sentinels, the local coordinator can use
// same-lane peers, but the UI must label the resulting confidence as peer-only and
// must not silently promote a participant on insufficient evidence."
//
// Peer-only is not bad evidence. It is the evidence a mailbox's own lane-mates
// produced, and for a healthy pool that is most of it. What it is not is
// INDEPENDENT: when a `watch` mailbox is measured only by other `watch` mailboxes, a
// shared cause moves both sides of the comparison at once, and the reading looks
// stable while nothing about it is.
type EvidenceConfidence string

const (
	// ConfidencePeerOnly means every observation came from same-lane peers.
	ConfidencePeerOnly EvidenceConfidence = "peer_only"
	// ConfidenceSentinelCorroborated means at least one sentinel observed this
	// mailbox's mail, so the reading has a controlled reference point.
	ConfidenceSentinelCorroborated EvidenceConfidence = "sentinel_corroborated"
)

// ConfidenceOf labels a body of evidence by whether a sentinel contributed to it.
//
// Deliberately a LABEL and not a multiplier. It would be easy to discount peer-only
// evidence — require more samples, or scale a rate — and that is a threshold change
// with no calibration behind it: nobody has yet measured how much a sentinel
// observation is worth relative to a peer one in this system. Every prior slice that
// guessed a threshold and acted on it had to be walked back (see security.md
// invariants 57 through 60). So this reports the distinction and changes no decision,
// and the operator sees which kind of evidence promoted their mailbox.
func ConfidenceOf(sentinelObservations int) EvidenceConfidence {
	if sentinelObservations > 0 {
		return ConfidenceSentinelCorroborated
	}
	return ConfidencePeerOnly
}

// SentinelPoolShare is the most of a workspace's pool that may be sentinels before
// the arrangement stops being a measurement and becomes the network.
//
// The design (§4) requires sentinels be "capped, monitored, rotated, and retired so
// they cannot become a recognizable high-volume seed network". This is the cap half,
// and it is advisory: exceeded, it is reported, never enforced. Refusing to pair
// would be the wrong response to an operator having designated too many — it would
// stop warmup rather than tell them something, and this file has no business making
// that call.
const SentinelPoolShare = 0.5

// SentinelPoolOversized reports whether sentinels have grown past the advisory share
// of a pool. Both counts are of ENABLED participants; a pool of one sentinel and
// nothing else is oversized by this measure and is also useless, which is worth
// saying to an operator rather than hiding.
func SentinelPoolOversized(sentinels, pool int) bool {
	if pool <= 0 {
		return false
	}
	return float64(sentinels)/float64(pool) > SentinelPoolShare
}
