package warmup

import (
	netmail "net/mail"
	"net/netip"
	"strings"
)

// Observed relay extraction: which MTA a warmup message actually arrived from.
//
// This is the last of design §3.3's fault dimensions that can be read off a
// message. It answers "these three domains fail through one relay" by NAME
// instead of by inference — but only when the answer comes from a hop the
// RECEIVER wrote, because a `Received` chain is a list the sender partly controls.
//
// It shares identity.go's two load-bearing properties, for the same reasons:
//
//   - It runs only on token-verified warmup mail, inside the poller's warmupValid
//     branch, after the DB has re-proven the send↔recipient binding.
//   - It cannot fail. There is no error return and no panic path. A failure that
//     propagated would abort the receipt, and an aborted receipt returns before
//     SetInboxCursor — wedging the poll cursor and stopping ALL inbound processing
//     for that mailbox, campaign replies and bounce detection included. That
//     failure shape has already shipped once.
//
// ASN resolution is deliberately absent. Mapping an address to an autonomous
// system needs a MaxMind-class dataset, which for a self-hostable product is a new
// dependency, a licence question and a refresh story all at once. The address is
// recorded; the autonomous system it belongs to is a later slice's problem, and it
// needs an answer to those three questions before it needs code.

// maxReceivedHopsScanned bounds how far down the chain the scan may look.
//
// The number of `Received` headers is chosen by whoever sent the message, so it is
// externally influenceable and an unbounded scan is work an outsider gets to
// specify. The bound costs nothing real: the scan stops at the first hop that
// names a peer (see ObservedRelayIP), and the receiving infrastructure prepends
// only a handful of hops above that one — two for Gmail, three or four for
// Exchange Online. A hop deeper than this is not one the receiver wrote.
const maxReceivedHopsScanned = 8

// cgnatBlock is RFC 6598's shared address space. netip has a predicate for every
// other range this file refuses but not for this one.
//
// MustParsePrefix on a literal cannot fail, so it does not break this file's
// "cannot fail" contract; it is evaluated once at init, not per message.
var cgnatBlock = netip.MustParsePrefix("100.64.0.0/10")

// ObservedRelayIP is the address the RECEIVING system observed its peer
// connecting from, in one canonical spelling, or "" when nothing trustworthy
// could be read. Pure: no clock, no I/O, no DNS.
//
// **The hop-selection rule, which is the whole of this function's security:**
//
//  1. The receiving MTA PREPENDS, so the receiver's own hops are at the top and
//     everything below them is progressively less trustworthy. The scan therefore
//     runs top-down and STOPS at the first hop that names a peer at all.
//  2. Only a hop that names NO peer is skipped. Gmail prepends exactly such a hop
//     ("Received: by 2002:a05:...", no `from` clause) above its own MX hop, so the
//     skip is needed — and it is safe in a way a general search is not, because a
//     hop that names no peer cannot be the answer under any rule. Skipping it
//     discards nothing, which is the property trustedAuthResults' correction turns
//     on: it refused to walk PAST a candidate it could not vouch for, and neither
//     does this.
//  3. The hop that stops the scan must be one the receiver wrote: its `by` host has
//     to fold to a domain derived from OUR OWN record of the mailbox. That is
//     receiverStamped, unchanged and deliberately not re-derived here — its
//     parameter is a hostname and the question ("does this name identify the
//     receiving system") is identical to the one RFC 8601 §5 asks of an
//     authserv-id. A second allowlist is the "two things that must agree" shape
//     every repeated defect in this subsystem has taken.
//  4. The address is read only from the from clause's ONE comment, never from the
//     `from` token itself. The token is the sender's HELO argument — an attacker may
//     set it to "[198.51.100.7]" and it is a legal address literal — while the
//     parenthesised comment beside it is what the receiver observed and wrote.
//     Comments belonging to the `by` clause are excluded too: those describe the
//     receiver's own side. See observedAddress on why a from clause carrying more
//     than one comment is refused rather than searched.
//  5. A peer that folds to the receiver's own domains is refused. It is the
//     receiver's infrastructure, not a relay identity, and recording it would file
//     every message a provider delivered under one shared pseudo-relay — inventing
//     a correlation that is really just a shared destination, which
//     warmup_observations.destination_esp already records honestly.
//
// **Why every failure direction is "" and never a guess.** Each rule above can only
// move the result toward "": an attacker who chooses a HELO that folds to the
// receiver's own domain suppresses their own address (rule 5) rather than choosing
// one, because rule 1 stops rather than skipping and so never reaches a hop they
// wrote. Suppression is acceptable; substitution would not be.
//
// **The blind spots are known and preferred to a guess.** Exchange Online prepends
// internal hops that DO name a peer — its own mailbox servers — so an M365 receiver
// yields "" rather than Microsoft's internal frontend address. Skipping those to
// reach the ingress hop below is exactly the walk rule 1 forbids, and it is
// exploitable with one EHLO string: an attacker whose HELO folds to the receiver's
// provider makes the genuine ingress hop look internal, and a scan that skipped it
// would land on a hop the attacker wrote. Gmail-to-Gmail delivery never crosses an
// SMTP boundary and so names no peer anywhere near the top; a consumer mailbox
// polled over raw IMAP can vouch for no hop at all. All three are honestly "",
// which is the same trade identity.go makes for a provider that stamps no verdicts.
//
// **This gates nothing.** No threshold, lane, health state or promotion decision
// may read the result, and the temptation is obvious — "three degraded senders
// share one relay" reads like something to act on. It is not, for the reason
// security.md invariants 57-60 give for the dimensions already here: an
// attacker-influenceable identity that reaches pool eligibility is the escalation
// path. Reaching this code needs a live warmup token, but the value it produces is
// influenceable by anyone who can deliver a copy of a token-carrying message to one
// warmup recipient, which is a strictly weaker actor than invariant 57's MX
// controller. Suppression is the influence available (see above), and a dimension an
// attacker can blank at will must not be able to change which mailboxes may send.
func ObservedRelayIP(h netmail.Header, r Receiver) string {
	values := headerValues(h, "Received")
	if len(values) > maxReceivedHopsScanned {
		values = values[:maxReceivedHopsScanned]
	}
	for _, value := range values {
		hop, namesPeer := parseReceived(value)
		if !namesPeer {
			continue // rule 2: this hop states no peer, so there is nothing in it to read
		}
		// Rule 3. An empty by host fails this, which is the intent: a hop with no
		// `by` clause names no receiver.
		if !receiverStamped(hop.byHost, r) {
			return ""
		}
		// Rule 5. An empty peer name folds to "" and is not a match, so a hop whose
		// from clause is only a comment is judged on its address alone.
		if receiverStamped(hop.peerName, r) {
			return ""
		}
		return routableAddress(hop.observedAddress())
	}
	return ""
}

// receivedHop is the part of one `Received` header this file reasons about.
//
// peerName is kept SEPARATE from the comments, and never consulted for an address,
// because the two have different authors: the name is the sender's HELO argument
// and the comments are the receiver's own note of what it saw.
type receivedHop struct {
	peerName string   // the HELO argument — sender-chosen, used only for rule 5
	comments []string // comments inside the from clause, in the order they appeared
	byHost   string   // the host that claims to have received this hop
}

// observedAddress is the address the receiver recorded for its peer.
//
// **Exactly one comment, or nothing.** Every real MTA writes precisely one comment
// in the from clause — Gmail's "(rdns. [ip])", Postfix's "(rdns [ip])", Exchange's
// "(ip)" — and requiring that closes the one substitution this file would otherwise
// be open to. The HELO argument is the sender's string and it sits immediately
// before that comment, so a HELO containing a parenthesised group of its own
// ("x ([198.51.100.7])") produces a SECOND comment that a "first one wins" search
// would read INSTEAD of the receiver's. Refusing an ambiguous from clause turns
// that from a chosen value into no value. It costs the two-comment shape some old
// MTAs emit ("from x (HELO x) (203.0.113.25)"), which is a suppression, and this
// file prefers suppression to substitution everywhere else too.
//
// Within that one comment: bracketed first, because "[203.0.113.25]" and
// "[IPv6:2001:db8::1]" are RFC 5321 §4.1.3 address literals and unambiguous. A bare
// comment is accepted only when the WHOLE of it is an address — the form Exchange
// writes. That exactness is the point: "(port=25 helo=203.0.113.25)" must not
// contribute an address, because the helo parameter is the sender's string.
//
// The FIRST bracketed token wins even when it turns out to be unusable — a private
// address, or not an address at all. Scanning on for a better candidate is the
// walk-past-the-genuine-value shape this file exists to avoid, in miniature, and it
// is the same discipline parseVerdicts applies when a first verdict normalizes to
// unknown.
func (hop receivedHop) observedAddress() string {
	if len(hop.comments) != 1 {
		return ""
	}
	comment := hop.comments[0]
	if literal, found := bracketedLiteral(comment); found {
		return literal
	}
	bare := strings.TrimSpace(comment)
	if _, err := netip.ParseAddr(bare); err != nil {
		return ""
	}
	return bare
}

// ipv6LiteralTag prefixes an IPv6 address literal per RFC 5321 §4.1.3.
const ipv6LiteralTag = "ipv6:"

// bracketedLiteral returns the contents of the first bracketed group in a comment,
// with any IPv6 tag removed, and whether there was one. The contents are NOT
// validated here — see observedAddress on why the first group wins regardless.
func bracketedLiteral(comment string) (string, bool) {
	open := strings.IndexByte(comment, '[')
	if open < 0 {
		return "", false
	}
	end := strings.IndexByte(comment[open+1:], ']')
	if end < 0 {
		return "", false // an unterminated bracket states nothing
	}
	inner := strings.TrimSpace(comment[open+1 : open+1+end])
	if len(inner) >= len(ipv6LiteralTag) && strings.EqualFold(inner[:len(ipv6LiteralTag)], ipv6LiteralTag) {
		inner = inner[len(ipv6LiteralTag):]
	}
	return inner, true
}

// parseReceived splits one `Received` header into the pieces the hop rule needs,
// and reports whether the hop NAMES A PEER at all.
//
// The second return is the scan's stop condition, so its two false-y cases are
// deliberately different. A hop with no `from` clause names no peer and is skipped
// (false). A STRUCTURALLY DAMAGED hop is a claim that cannot be read, not an absent
// claim, so it reports true with nothing filled in: the scan stops there and yields
// "" instead of continuing to a hop further down, which is where a forgery would
// be. Discarding a genuinely malformed hop is the cost, and "" gates nothing.
func parseReceived(value string) (receivedHop, bool) {
	tokens, wellFormed := tokenizeReceived(value)
	if !wellFormed {
		return receivedHop{}, true
	}
	fromIdx := indexOfKeyword(tokens, "from", 0)
	if fromIdx < 0 {
		return receivedHop{}, false
	}
	// RFC 5321 §4.4 orders the clauses from / by / via / with / id / for, so the
	// from clause is everything between the two keywords. Bounding the comment
	// collection at `by` is what keeps the receiver's own side out of the address
	// search (rule 4).
	byIdx := indexOfKeyword(tokens, "by", fromIdx+1)
	if byIdx < 0 {
		byIdx = len(tokens)
	}

	var hop receivedHop
	for _, t := range tokens[fromIdx+1 : byIdx] {
		switch {
		case t.comment:
			hop.comments = append(hop.comments, t.text)
		case hop.peerName == "":
			hop.peerName = hostToken(t.text)
		}
	}
	for _, t := range tokens[min(byIdx+1, len(tokens)):] {
		if !t.comment {
			hop.byHost = hostToken(t.text)
			break
		}
	}
	return hop, true
}

// indexOfKeyword finds a bare (non-comment) word token at or after start. Keyword
// comparison is case-insensitive: RFC 5321's grammar keywords are, and real MTAs
// vary.
func indexOfKeyword(tokens []receivedToken, keyword string, start int) int {
	for i := start; i < len(tokens); i++ {
		if !tokens[i].comment && strings.EqualFold(tokens[i].text, keyword) {
			return i
		}
	}
	return -1
}

// hostToken normalises a word token that is being read as a hostname.
//
// The trailing punctuation matters in practice: a minimal hop ends the by clause
// with the date separator ("by mx.google.com; Tue, ..."), and a fully-qualified
// name may carry the root dot. Left in place, both make the host fail the
// receiver check — a coverage loss that looks exactly like a provider we cannot
// attribute. receiverStamped trims the root dot too; doing it here as well costs
// nothing and keeps this function's output independent of that.
func hostToken(token string) string {
	return strings.TrimRight(strings.ToLower(strings.TrimSpace(token)), ".,;")
}

// receivedToken is one lexical piece of a `Received` value: either a depth-0 word
// or the contents of one top-level comment.
type receivedToken struct {
	text    string
	comment bool
}

// tokenizeReceived splits a `Received` value into words and comments, and reports
// whether it was structurally sound.
//
// Comments are KEPT rather than stripped, which is why this cannot reuse
// stripComments: the address this file wants is inside one. The bookkeeping is the
// same and for the same reasons — comments nest, a quoted string inside one may
// hold an unbalanced parenthesis, and a backslash escapes the next byte in both, so
// all three are tracked instead of scanning for a matching ')'.
//
// Any structural anomaly makes the whole value unusable, matching stripComments and
// for a related reason: an attacker whose HELO name lands inside the receiver's own
// comment can close that comment EARLY with a ')' and have the rest of their text
// read as depth-0 words — which is how a forged `by` token would get in. Refusing
// an unbalanced close, an unterminated comment or an unterminated quote catches it,
// at the cost of discarding genuinely malformed hops.
func tokenizeReceived(value string) ([]receivedToken, bool) {
	var out []receivedToken
	var buf []byte
	depth, quoted := 0, false

	flush := func(isComment bool) {
		if len(buf) > 0 {
			out = append(out, receivedToken{text: string(buf), comment: isComment})
			buf = buf[:0]
		}
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		// A quoted-pair escapes whatever follows, so neither the backslash nor the
		// escaped byte can close a string or a comment.
		if c == '\\' && i+1 < len(value) && (quoted || depth > 0) {
			buf = append(buf, c, value[i+1])
			i++
			continue
		}
		switch {
		case quoted:
			buf = append(buf, c)
			if c == '"' {
				quoted = false
			}
		case c == '"':
			buf = append(buf, c)
			quoted = true
		case c == '(':
			if depth == 0 {
				flush(false) // the word before a comment ends at it
			} else {
				buf = append(buf, c) // a nested comment's parens are content
			}
			depth++
		case c == ')':
			if depth == 0 {
				return nil, false // unbalanced close — see the note above
			}
			depth--
			if depth == 0 {
				flush(true)
			} else {
				buf = append(buf, c)
			}
		case depth == 0 && (c == ' ' || c == '\t'):
			flush(false)
		default:
			buf = append(buf, c)
		}
	}
	if depth > 0 || quoted {
		return nil, false
	}
	flush(false)
	return out, true
}

// routableAddress returns the canonical spelling of an address that could identify
// a relay, and "" for anything that could not.
//
// Normalisation is the reason this returns a string rather than the input: the same
// relay must be ONE value, so "2001:0DB8::0001", "2001:db8::1" and a zone-suffixed
// form all have to converge, and an IPv4-mapped literal has to converge with the
// IPv4 address it maps. netip's canonical form (RFC 5952) does that, and it also
// bounds the stored string to 45 bytes — so unlike the header-derived domains
// beside it in this table, no length guard is needed.
//
// The refused ranges are not a hygiene list. Private, loopback, link-local, CGNAT,
// multicast and unspecified addresses are addresses an attacker can name freely and
// which identify no infrastructure: two workspaces' "10.0.0.1" are unrelated
// machines, so recording one would merge unrelated senders into a single phantom
// fault domain. Documentation ranges are deliberately NOT refused — they are
// globally unique assignments and refusing them would only make this untestable
// with realistic values.
func routableAddress(s string) string {
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return ""
	}
	// Unmap before every predicate below: an IPv4-mapped literal must be judged as
	// the IPv4 address it is, or "[::ffff:10.0.0.1]" would pass the private check.
	addr = addr.Unmap().WithZone("")
	switch {
	case !addr.IsValid(), addr.IsUnspecified(), addr.IsLoopback(), addr.IsPrivate(),
		addr.IsLinkLocalUnicast(), addr.IsLinkLocalMulticast(),
		addr.IsInterfaceLocalMulticast(), addr.IsMulticast():
		return ""
	case cgnatBlock.Contains(addr):
		return ""
	}
	return addr.String()
}
