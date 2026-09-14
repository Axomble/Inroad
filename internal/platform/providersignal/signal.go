// Package providersignal turns what a mailbox PROVIDER told us into a small
// typed vocabulary, and accumulates those verdicts per worker as window deltas.
//
// It exists because of an asymmetry the fleet design rests on: Inroad never
// delivers to a recipient's MX. Every send authenticates to the CUSTOMER's own
// provider — their SMTP relay, the Gmail API, or Microsoft Graph — and that
// provider delivers from its own outbound pool. The recipient therefore never
// sees a worker's egress IP, so recipient-side reputation (bounces, complaints,
// spam placement — internal/platform/warmup) cannot transfer between mailboxes
// that share a worker.
//
// What the worker's IP IS visible to is the provider: it authenticates from that
// address on every send and every poll, and providers throttle, challenge and
// rate-limit per source address. That is the real per-IP risk, and this package
// is the only place it is measured.
//
// Nothing here does I/O, holds a lock longer than a map write, or knows what a
// database is. Classification happens HERE, at the capture point, where the
// provider's reply is still in hand — never in SQL, which would push the parsing
// onto every reader and every future query.
package providersignal

// Reason is one classified provider verdict. The set is CLOSED: it is mirrored
// by a CHECK constraint on worker_provider_signals.reason, so a value outside it
// fails the batch insert and loses a whole window of counts. Classify is
// therefore required to return only members of this set (asserted by
// TestClassifyOnlyReturnsKnownReasons), and Normalize is the belt-and-braces
// second half of that rule for anything arriving from outside this package.
type Reason string

const (
	// ReasonOK is a completed operation. It is the denominator half of
	// "attempts vs successes": attempts are the SUM over every reason for an
	// operation, successes are this one.
	ReasonOK Reason = "ok"
	// ReasonAuthFailed is the provider refusing our credentials (SMTP 530/534/535,
	// enhanced 5.7.8; HTTP 401). It is NOT by itself an IP signal — a rotated app
	// password looks identical — but a rate of it that rises across MANY mailboxes
	// on one worker is, which is why it is counted per worker rather than per
	// mailbox.
	ReasonAuthFailed Reason = "auth_failed"
	// ReasonRateLimited is the provider naming a RATE as the cause: enhanced
	// 4.7.0 (per-auth / per-connection rate) or 4.7.28 (per-IP rate), HTTP 429, or
	// an HTTP 403 whose machine reason names a rate or quota. This is the signal
	// the fleet is being built to act on.
	ReasonRateLimited Reason = "rate_limited"
	// ReasonThrottled is a transient refusal the provider did NOT attribute to a
	// rate: a bare 4xx, a 4.7.x that is neither of the two rate codes, HTTP 5xx.
	// Kept distinct from rate_limited on purpose — merging them would let ordinary
	// congestion read as reputation pushback.
	ReasonThrottled Reason = "throttled"
	// ReasonBlocked is a PERMANENT security/policy refusal (enhanced 5.7.x other
	// than 5.7.8, HTTP 403 with no rate reason). This is the shape a provider uses
	// when it has decided against the connecting identity, so it is the strongest
	// per-IP signal in the vocabulary.
	//
	// It deliberately does not distinguish "your IP is listed" from "daily user
	// sending limit exceeded", both of which some providers send as 5.7.0: telling
	// them apart requires reading the prose, which is exactly what this package
	// refuses to do. Both mean "stop sending from here".
	ReasonBlocked Reason = "blocked"
	// ReasonRejected is a permanent NON-security refusal — a bad recipient, an
	// oversized message, a malformed request. It is counted so that attempts and
	// successes reconcile, and it must NOT be read as per-IP risk: a mailing list
	// full of dead addresses produces it in volume and says nothing about the
	// worker.
	ReasonRejected Reason = "rejected"
	// ReasonUnreachable is a network-layer failure — the provider never replied at
	// all (dial timeout, refused, reset, EOF, deadline). Still a per-IP fact: an
	// egress address a provider has stopped accepting connections from fails here,
	// not with a reply code.
	ReasonUnreachable Reason = "unreachable"
	// ReasonOther is everything we could not attribute to the provider: a
	// message-build failure, an SSRF rejection (our own decision, not theirs), a
	// cancelled context (our own shutdown), or a transport whose error type
	// carries no structured reply. A rising "other" means this package has gone
	// blind, which is itself worth seeing.
	ReasonOther Reason = "other"
)

// Operation is which provider interaction produced a Reason. It exists because
// an auth failure on a poll and one on a send are the same fact about the IP but
// different facts about the fleet: without it, "attempts vs successes" for sends
// cannot be computed at all, since poll outcomes would pool into the same
// counters.
type Operation string

const (
	// OpSend is an outbound message handed to the provider.
	OpSend Operation = "send"
	// OpPoll is a mailbox read (IMAP SELECT/FETCH and its login).
	OpPoll Operation = "poll"
)

// Provider names the transport leg that actually ran.
type Provider string

const (
	ProviderSMTP  Provider = "smtp"
	ProviderGmail Provider = "gmail"
	ProviderM365  Provider = "m365"
)

// NormalizeProvider maps a job's provider field onto the leg that actually
// dialed, using the SAME rule mail.MultiSender dispatches on: "gmail" and "m365"
// select their API transports and EVERYTHING ELSE (including an empty string)
// takes the SMTP path. Keeping the two in step is what makes the recorded label
// true — it names the transport that ran, not the string that was stored.
func NormalizeProvider(s string) Provider {
	switch Provider(s) {
	case ProviderGmail:
		return ProviderGmail
	case ProviderM365:
		return ProviderM365
	default:
		return ProviderSMTP
	}
}

// Known reports whether r is a member of the persisted vocabulary.
func (r Reason) Known() bool {
	switch r {
	case ReasonOK, ReasonAuthFailed, ReasonRateLimited, ReasonThrottled,
		ReasonBlocked, ReasonRejected, ReasonUnreachable, ReasonOther:
		return true
	default:
		return false
	}
}

// Known reports whether op is a member of the persisted vocabulary.
func (op Operation) Known() bool { return op == OpSend || op == OpPoll }
