package warmup

import (
	netmail "net/mail"
	"net/textproto"
	"strings"
)

// Identity extraction: who sent this warmup mail, and what the receiving
// provider concluded about it.
//
// Everything here parses headers carried INSIDE a message, which is the input
// category behind both live findings this subsystem has produced (a forged DSN
// whose Original-Message-ID was the entire authorization check, and a forged
// warmup token). Two properties keep it tractable, and both are load-bearing:
//
//   - It runs only on token-verified warmup mail, inside the poller's
//     warmupValid branch, after the DB has re-proven the send↔recipient binding.
//     An external party cannot reach this code at all.
//   - It cannot fail. Every function below returns a value, never an error. A
//     parse failure that propagated would abort the receipt, and an aborted
//     receipt returns before SetInboxCursor — wedging the poll cursor and
//     stopping ALL inbound processing for that mailbox, campaign replies and
//     bounce detection included. That failure shape has already shipped once.
//
// None of it gates anything. See ExtractIdentity's contract note.

// The verdict vocabulary, closed deliberately. Anything a receiver reports that
// is not one of the first four becomes AuthUnknown rather than being stored
// verbatim: these are policy inputs for a later slice, not a log line.
const (
	AuthPass    = "pass"
	AuthFail    = "fail"
	AuthNeutral = "neutral"
	AuthNone    = "none"
	AuthUnknown = "unknown"
)

// Receiver is the mailbox that RECEIVED the message being parsed — the polled
// mailbox, not the sender.
//
// Provider is here, and not merely Address, because the provider allowlist in
// receiverStamped is unsafe without it. See that function.
type Receiver struct {
	Address  string // the polled mailbox's own email address
	Provider string // "gmail" | "m365" | "smtp"
}

// Identity is the sending identity a warmup message carried and the receiving
// provider's verdicts on it.
//
// Domains are "" when absent or unparseable — absent and unparseable are the
// same fact to every consumer, and one representation avoids a three-way
// condition at each read. Verdicts are always one of the five Auth* constants,
// never empty.
type Identity struct {
	DKIMDomain       string
	ReturnPathDomain string
	SPFResult        string
	DKIMResult       string
	DMARCResult      string
}

// UnknownIdentity is what a message yields when nothing could be established:
// no signature, no return path, and no trustworthy verdicts. It is also the
// correct value for any caller that has no header to parse at all, which is why
// it is exported rather than left as an implicit zero value — the zero Identity
// has EMPTY verdicts, and empty is not a member of the vocabulary above.
func UnknownIdentity() Identity {
	return Identity{SPFResult: AuthUnknown, DKIMResult: AuthUnknown, DMARCResult: AuthUnknown}
}

// ExtractIdentity reads the sending identity and the receiver's authentication
// verdicts out of a received warmup message. Pure: no clock, no I/O, no DB.
//
// It NEVER returns an error and never panics on a malformed header; every
// failure path yields an empty domain or an unknown verdict. See the package
// note above for why that is a correctness requirement and not politeness.
//
// **This gates nothing.** No threshold, lane, health state, or promotion
// decision may read any field of the result, and the temptation to wire one up
// is obvious — a DMARCResult of "fail" looks actionable. It is not, for three
// reasons. The verdicts are AuthUnknown for every provider that does not stamp
// results, so gating would penalise a whole provider class for our inability to
// observe it. Authentication posture is already gated, correctly and
// separately, by sending_domains and the pending_auth lane, which act on DNS we
// resolve ourselves rather than on a header a message carried. And these
// identities exist so a later slice can say "these three domains fail through
// one relay" — gating on them individually now would fire per-mailbox alarms for
// exactly the correlated failures that slice is meant to group.
func ExtractIdentity(h netmail.Header, r Receiver) Identity {
	id := UnknownIdentity()
	id.DKIMDomain = dkimSigningDomain(h)
	id.ReturnPathDomain = returnPathDomain(h)
	if v, ok := trustedAuthResults(h, r); ok {
		id.SPFResult, id.DKIMResult, id.DMARCResult = v.spf, v.dkim, v.dmarc
	}
	return id
}

// headerValues returns EVERY value for a header, topmost first.
//
// netmail.Header.Get returns only the first, which is wrong here: a message can
// carry several Authentication-Results and choosing between them is the security
// decision this file exists to make. The map must be indexed by the canonical
// MIME key — "DKIM-Signature" is stored as "Dkim-Signature", so a literal lookup
// silently finds nothing.
//
// Order matters and is preserved: net/textproto appends values in the order they
// appeared, so index 0 is the topmost header in the message.
func headerValues(h netmail.Header, key string) []string {
	return h[textproto.CanonicalMIMEHeaderKey(key)]
}

// dkimSigningDomain is the d= tag of the first DKIM-Signature that yields one.
//
// A multi-signature message (a forwarder adds its own, or a domain signs with
// two selectors) genuinely has several signing identities. Recording the first
// is a deliberate simplification, not an assumption that there is only one;
// representing all of them is a later slice's problem, and it needs a row shape
// this column does not have.
func dkimSigningDomain(h netmail.Header) string {
	for _, sig := range headerValues(h, "DKIM-Signature") {
		if d := tagValue(sig, "d"); d != "" {
			return strings.ToLower(strings.TrimSuffix(d, "."))
		}
	}
	return ""
}

// tagValue reads one tag out of an RFC 6376 tag-list ("v=1; a=rsa-sha256; d=x").
//
// Tag names are case-sensitive per RFC 6376 §3.2, so this compares them exactly.
// Folding whitespace may appear anywhere in a value, including in the middle of
// one, so the value has ALL whitespace removed rather than merely being trimmed.
func tagValue(taglist, name string) string {
	for _, part := range strings.Split(taglist, ";") {
		k, v, ok := strings.Cut(part, "=")
		if !ok || strings.TrimSpace(k) != name {
			continue
		}
		return strings.Join(strings.Fields(v), "")
	}
	return ""
}

// returnPathDomain is the host of the Return-Path address.
//
// The EXACT host, not the eTLD+1 — the opposite of what the mailbox reputation
// gate does, and deliberately so. domain.go's rule is that SPF/DKIM/DMARC are
// published per host, so "which DNS name is this" is the right question for an
// authentication identity, while "which mailboxes share a reputation" is the
// right question for the gate. Folding bounce.example.com into example.com here
// would erase precisely the distinction the fault-domain slices need: one
// signing or bouncing identity failing while its siblings are fine.
func returnPathDomain(h netmail.Header) string {
	raw := strings.TrimSpace(h.Get("Return-Path"))
	if raw == "" {
		return ""
	}
	if addr, err := netmail.ParseAddress(raw); err == nil {
		return addressHost(addr.Address)
	}
	// "<>" — the null return path every bounce carries — is not a parseable
	// address, and neither are several forms real MTAs emit. Strip the angle
	// brackets and take the host directly rather than discarding a domain that is
	// plainly there. A null return path has no host and correctly yields "".
	return addressHost(strings.Trim(raw, "<> \t"))
}

// authVerdicts is one Authentication-Results header's three interesting methods.
type authVerdicts struct{ spf, dkim, dmarc string }

// trustedAuthResults returns the verdicts from the topmost Authentication-Results
// header the RECEIVING system stamped, and false when there is no such header.
//
// A message can carry several, and every one below the receiving boundary is
// sender-influenceable: an actor who reached this code could add a "dkim=pass" of
// their own. RFC 8601 §5 makes the authserv-id the discriminator, so this trusts
// only a header whose authserv-id identifies the receiver. Scanning topmost-first
// is what makes that safe in practice — a receiving MTA PREPENDS its header, so a
// forged one necessarily sits below the genuine one.
//
// No match means all three verdicts stay unknown. Never pass, and never fail:
// absence of a verdict is not a verdict, which is the discipline the rest of this
// engine already follows. A mailbox whose provider stamps nothing is therefore
// permanently unknown on this axis. That is honest, and it is the reason none of
// this may gate anything.
func trustedAuthResults(h netmail.Header, r Receiver) (authVerdicts, bool) {
	for _, raw := range headerValues(h, "Authentication-Results") {
		id, methodspecs, ok := strings.Cut(stripComments(raw), ";")
		if !ok {
			continue // an authserv-id with no methodspecs states nothing
		}
		if !receiverStamped(authservID(id), r) {
			continue
		}
		return parseVerdicts(methodspecs), true
	}
	return authVerdicts{}, false
}

// authservID is the identifier at the head of an Authentication-Results header,
// dropping the optional version number that may follow it ("mx.google.com 1;").
func authservID(head string) string {
	fields := strings.Fields(head)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// providerAuthservDomains are the authserv-id domains each hosted provider stamps
// with. Keyed BY PROVIDER, which is the whole point — see receiverStamped.
var providerAuthservDomains = map[string][]string{
	"gmail": {"google.com"},
	"m365":  {"outlook.com", "microsoft.com"},
}

// receiverStamped reports whether an authserv-id identifies the system that
// received this message.
//
// Two ways to qualify, and the second is the one with a trap in it:
//
//  1. The authserv-id shares an organizational domain with the recipient's own
//     address — a self-hosted receiver whose MX lives in its own domain.
//  2. It is a known identifier of the recipient's OWN provider.
//
// Rule 2 is gated on r.Provider, and that gate is the security property. A bare
// allowlist — "trust mx.google.com" — is exploitable: an attacker delivering to
// an IMAP mailbox, which stamps no Authentication-Results at all, could put a
// forged "Authentication-Results: mx.google.com; dkim=pass" at the top of the
// message and it would be the only candidate, so it would be believed. Requiring
// the recipient to actually BE on Gmail removes that: a real Gmail mailbox always
// has Gmail's own header above the forged one, and an IMAP mailbox never consults
// the allowlist.
//
// The residual, stated plainly: a self-hosted receiver that stamps nothing, whose
// attacker forges an authserv-id in the recipient's own domain, is believed. That
// cannot be closed without knowing the receiving MTA's identity, which we do not.
// It is bounded by the fact that reaching this code needs a live warmup token, so
// the mitigation is token secrecy — the same residual placement already carries.
func receiverStamped(authservID string, r Receiver) bool {
	host := registrableHost(strings.ToLower(strings.TrimSuffix(authservID, ".")))
	if host == "" {
		return false
	}
	if own := OrganizationalDomain(r.Address); own != "" && host == own {
		return true
	}
	for _, d := range providerAuthservDomains[strings.ToLower(r.Provider)] {
		if host == d {
			return true
		}
	}
	return false
}

// parseVerdicts reads the spf/dkim/dmarc results out of the methodspec list of a
// header already established to be the receiver's.
//
// The FIRST occurrence of each method wins, including when it normalizes to
// unknown — a later "dkim=pass" must not overwrite an earlier "dkim=fail" that
// this vocabulary could not represent, because that would upgrade a negative into
// a positive by way of a parsing gap.
func parseVerdicts(methodspecs string) authVerdicts {
	v := authVerdicts{spf: AuthUnknown, dkim: AuthUnknown, dmarc: AuthUnknown}
	var seenSPF, seenDKIM, seenDMARC bool
	for _, spec := range strings.Split(methodspecs, ";") {
		// The result is the FIRST token of a methodspec; anything after it is a
		// ptype.property assignment ("dkim=pass header.i=@example.com") describing
		// the result rather than restating it.
		fields := strings.Fields(spec)
		if len(fields) == 0 {
			continue
		}
		method, result, ok := strings.Cut(fields[0], "=")
		if !ok {
			continue
		}
		// A method may carry a version ("dkim/1=pass").
		method, _, _ = strings.Cut(strings.ToLower(method), "/")
		switch method {
		case "spf":
			if !seenSPF {
				v.spf, seenSPF = normalizeAuthResult(result), true
			}
		case "dkim":
			if !seenDKIM {
				v.dkim, seenDKIM = normalizeAuthResult(result), true
			}
		case "dmarc":
			if !seenDMARC {
				v.dmarc, seenDMARC = normalizeAuthResult(result), true
			}
		}
	}
	return v
}

// normalizeAuthResult maps a reported result onto the closed vocabulary.
//
// Everything outside it — softfail, temperror, permerror, policy, and whatever a
// provider invents next — becomes unknown rather than being forced toward the
// nearest neighbour. softfail is the interesting one: calling it "fail" would
// overstate a deliberately equivocal answer, and calling it "pass" would invent a
// result, so it joins the honest category. Widening the vocabulary later is a
// migration; mislabelling a verdict now is a defect that survives one.
func normalizeAuthResult(result string) string {
	switch strings.ToLower(strings.TrimSpace(result)) {
	case AuthPass:
		return AuthPass
	case AuthFail:
		return AuthFail
	case AuthNeutral:
		return AuthNeutral
	case AuthNone:
		return AuthNone
	default:
		return AuthUnknown
	}
}

// stripComments removes RFC 5322 comments and preserves everything else.
//
// Not cosmetic: comments are where providers put free text, and that text
// routinely contains the very delimiters this file splits on. Gmail emits
//
//	spf=pass (google.com: domain of x@y designates 1.2.3.4 as permitted sender)
//
// and a comment containing a ';' or an '=' would otherwise be parsed as another
// methodspec. Comments nest, quoted strings inside them may contain unbalanced
// parentheses, and a backslash escapes the next byte in both — so all three are
// tracked rather than the string being scanned for a matching ')'.
//
// An unterminated comment or quote consumes the remainder, which degrades to
// unknown verdicts. That is the safe direction, and the only alternative — parsing
// past the damage — is how a malformed header gets to assert a pass.
func stripComments(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	depth, quoted := 0, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\\' && i+1 < len(s) && (quoted || depth > 0) {
			if depth == 0 {
				b.WriteByte(c)
				b.WriteByte(s[i+1])
			}
			i++
			continue
		}
		switch {
		case quoted:
			if c == '"' {
				quoted = false
			}
			if depth == 0 {
				b.WriteByte(c)
			}
		case c == '"':
			quoted = true
			if depth == 0 {
				b.WriteByte(c)
			}
		case c == '(':
			depth++
		case c == ')':
			if depth == 0 {
				b.WriteByte(c) // unbalanced close: not a comment, so it is content
				continue
			}
			depth--
			if depth == 0 {
				b.WriteByte(' ') // a comment separates tokens; do not join them
			}
		case depth == 0:
			b.WriteByte(c)
		}
	}
	return b.String()
}
