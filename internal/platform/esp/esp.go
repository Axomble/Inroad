// Package esp classifies a mailbox or a recipient domain by the email service
// provider that actually handles its mail — Google, Microsoft, or neither.
// Sending Google→Google and Microsoft→Microsoft keeps a message inside one
// operator's infrastructure, which measurably helps placement; this package is
// the classifier that makes that pairing possible.
//
// Like platform/sendcap and platform/dnsauth it is narrow and pure: no database,
// no clock, and the only network access is through the injected Resolver, so
// every rule here is table-testable. Three judgements shape the API and must
// survive any refactor:
//
//  1. mailboxes.provider is a TRANSPORT tag, not an ESP tag. It is smtp|gmail|m365
//     and only selects which code path dials. A Google Workspace mailbox connected
//     by app password is provider='smtp', so classifying on provider alone would
//     silently mis-bucket a large share of real mailboxes. FromMailbox therefore
//     also reads smtp_host.
//  2. A miss is cheap, a wrong match is not. Relay-fronted senders (SendGrid,
//     Mailgun, a self-hosted Postfix in front of Workspace) read as Other, and
//     Other never matches anything — including another Other. "Other" is a
//     catch-all bucket, not shared infrastructure, so pairing two members of it
//     would be a coincidence dressed up as a decision.
//  3. Unknown is a first-class state, distinct from Other. Other means "checked,
//     and it is neither"; Unknown means "not checked yet". Both skip matching,
//     but only Unknown is worth re-checking, so the cache must be able to tell
//     them apart.
package esp

import (
	"context"
	"errors"
	"net"
	"strings"
	"time"
)

// lookupTimeout bounds a single MX lookup. Short on purpose, and for the same
// reason as dnsauth's: this runs in a sweep over a domain set bounded by the
// contact list, and a resolver that has not answered in this long is reported as
// Unknown rather than held onto.
const lookupTimeout = 5 * time.Second

// ESP is which operator handles a mailbox's or a domain's mail. It is a string
// because it is persisted verbatim (recipient_domains.esp), so there is one
// spelling of each value in the system rather than a mapping table per boundary.
type ESP string

const (
	// Unknown means no classification has been made yet — a lookup that has not
	// run, or one that failed. Never persisted as the outcome of a completed
	// check; see judgement 3.
	Unknown ESP = "unknown"
	// Google is Google Workspace or consumer Gmail.
	Google ESP = "google"
	// Microsoft is Microsoft 365 or consumer Outlook/Hotmail.
	Microsoft ESP = "microsoft"
	// Other means classified, and it is neither of the two. It never matches.
	Other ESP = "other"
)

// Matchable reports whether this classification can pair with another. Only
// Google and Microsoft can: Unknown has nothing to say and Other is a bucket,
// not a provider (judgements 2 and 3). Every caller that partitions a sender
// pool goes through here rather than testing != Unknown, so the Other rule
// cannot be forgotten at one call site.
func (e ESP) Matchable() bool { return e == Google || e == Microsoft }

// Valid reports whether s is one of the four states. The boundary check for a
// value read back out of Postgres or handed in by a caller; anything else is
// treated as Unknown rather than trusted.
func Valid(s string) bool {
	switch ESP(s) {
	case Unknown, Google, Microsoft, Other:
		return true
	default:
		return false
	}
}

// googleSMTPHosts are the submission endpoints that prove a provider='smtp'
// mailbox is really sending through Google. Suffix-matched so a subdomain form
// still hits, and deliberately short: this is a "definitely Google" list, not an
// attempt to enumerate every host Google owns. A host not on it reads as Other,
// which just skips matching.
var googleSMTPHosts = []string{"smtp.gmail.com", "smtp-relay.gmail.com"}

// microsoftSMTPHosts is the same idea for Microsoft 365.
var microsoftSMTPHosts = []string{"smtp.office365.com", "outlook.office365.com"}

// Transport tags as stored in mailboxes.provider. Plain strings rather than an
// import: platform/* must not depend on an app/* status vocabulary, and these
// three values are fixed by the mailboxes.provider CHECK constraint.
const (
	providerGmail = "gmail"
	providerM365  = "m365"
)

// FromMailbox classifies a sending mailbox from its transport tag and SMTP host.
//
// The API providers are conclusive: a gmail/m365 mailbox is dialing Google's or
// Microsoft's API and cannot be anything else. For an smtp mailbox the host is
// the only evidence there is, and anything unrecognised is Other — including an
// empty host, which is a misconfigured mailbox that will fail to send anyway.
// Never Unknown: this classification always completes, since it reads columns
// that are already in hand (judgement 3).
//
// It answers "who does this mailbox SUBMIT through". For "where was mail to this
// mailbox DELIVERED" — what a warmup destination route records — use
// FromRecipient: smtp_host is the outbound relay, and an smtp mailbox can submit
// through SendGrid while its inbound MX is Google Workspace.
func FromMailbox(provider, smtpHost string) ESP {
	switch provider {
	case providerGmail:
		return Google
	case providerM365:
		return Microsoft
	}
	host := normalizeHost(smtpHost)
	if host == "" {
		return Other
	}
	switch {
	case hasHostSuffix(host, googleSMTPHosts):
		return Google
	case hasHostSuffix(host, microsoftSMTPHosts):
		return Microsoft
	default:
		return Other
	}
}

// FromRecipient classifies where mail to a RECIPIENT mailbox was delivered, from
// that mailbox's transport tag and the recipient_domains MX cache's answer for
// its domain (cachedESP; "" for a miss).
//
// Not FromMailbox, and the difference is the whole point: FromMailbox reads
// smtp_host, which is the OUTBOUND relay. An smtp mailbox can submit through
// SendGrid while its inbound MX is Google Workspace, so classifying a delivery by
// the relay would file it under a destination the message never reached — and
// permanently, because the observation it lands on is immutable.
//
// The API providers are conclusive and outrank the cache: a gmail/m365 mailbox IS
// hosted there, so no MX record can contradict it. A Workspace tenant fronted by a
// third-party filter caches as Other (FromMX reads the primary MX) while the
// mailbox is still Google's — the provider is the better evidence in that case,
// not merely the cheaper one.
//
// For an smtp mailbox the cache is the only evidence there is, and a miss is
// Unknown — never Other, and never a DNS lookup. This runs on the warmup receipt
// path, where resolving would put a network round trip in front of a write whose
// failure wedges the poll cursor and stops ALL inbound processing for the mailbox.
// The recipientesp sweep does the resolving, off the hot path; until it has, the
// honest answer is "we have not resolved this domain".
//
// Pure by design: it is table-testable, and the two facts it reads (a transport
// tag, a cached string) are both already in hand at the call site.
func FromRecipient(provider, cachedESP string) ESP {
	switch provider {
	case providerGmail:
		return Google
	case providerM365:
		return Microsoft
	}
	// Validated rather than converted: cachedESP crosses a boundary (a Postgres
	// column, and one whose vocabulary a future migration could widen). An
	// unrecognised value read straight through would become a route of its own in
	// a matrix that GROUPs BY it, which is a silent failure; Unknown is the state
	// that already means "no classification to trust".
	//
	// UNREACHABLE TODAY, and kept deliberately. recipient_domains.esp carries its
	// own CHECK over the same four values (migration 000048) and the reader's JOIN
	// drops rows whose lookup never completed, so the only value that reaches here
	// outside the vocabulary is "", which the observation INSERT already coalesces
	// to 'unknown'. No test can exercise this branch without first widening that
	// CHECK — stated plainly rather than left to imply a live guard, because a
	// reader who assumed it was load-bearing might drop the SQL-side defences that
	// actually are.
	if !Valid(cachedESP) {
		return Unknown
	}
	return ESP(cachedESP)
}

// googleMXHosts are the MX suffixes Google publishes: Workspace
// (aspmx.l.google.com and its alt1..alt4 siblings, aspmx2..5.googlemail.com) and
// consumer Gmail (gmail-smtp-in.l.google.com).
var googleMXHosts = []string{"google.com", "googlemail.com"}

// microsoftMXHosts are Microsoft's: 365 tenants publish
// <tenant>.mail.protection.outlook.com and consumer Outlook/Hotmail publish
// under olc.protection.outlook.com.
var microsoftMXHosts = []string{"protection.outlook.com", "outlook.com", "hotmail.com"}

// FromMX classifies a RECIPIENT domain from its MX hosts, which must be in
// preference order (lowest preference first — what net.Resolver.LookupMX
// returns).
//
// The PRIMARY MX decides, and an unclassifiable primary is Other even when a
// later record is Google or Microsoft. The primary is the host a sender actually
// connects to, so a domain fronted by a third-party filter is that filter's
// domain from the sending side, whatever sits behind it — scanning down the list
// for a recognised name would report an infrastructure pairing that does not
// exist on the wire. A Google domain with a third-party BACKUP is unaffected:
// its primary is still Google.
//
// An empty set is Unknown, not Other: no MX means the lookup told us nothing,
// and "checked, and it is neither" would stop the sweep from ever retrying it.
func FromMX(mxHosts []string) ESP {
	for _, h := range mxHosts {
		host := normalizeHost(h)
		if host == "" {
			continue
		}
		switch {
		case hasHostSuffix(host, googleMXHosts):
			return Google
		case hasHostSuffix(host, microsoftMXHosts):
			return Microsoft
		default:
			return Other
		}
	}
	if len(mxHosts) == 0 {
		return Unknown
	}
	return Other
}

// Domain is the cache key for a recipient address: the lower-cased domain part.
//
// It must agree EXACTLY with the SQL the sweep derives its domain set from
// (lower(split_part(email, '@', 2)) in queries/recipientdomain.sql), or the send
// path would look up a key the sweep never writes and every read would miss.
// That is why it takes the segment after the FIRST '@' rather than the last:
// split_part's second field is what Postgres computes for a malformed
// "a@b@c.example", and matching it matters more than being clever about an
// address that cannot be delivered anyway. Returns "" when there is no domain.
//
// Lower-casing is the ONLY transform, for the same reason: normalizeHost's
// trimming would produce a key Postgres never writes.
func Domain(email string) string {
	parts := strings.Split(email, "@")
	if len(parts) < 2 {
		return ""
	}
	return strings.ToLower(parts[1])
}

// normalizeHost puts a hostname in the one form the suffix rules are written
// against: lowercase, unpadded, no trailing root dot (DNS answers carry one,
// configuration usually does not).
func normalizeHost(h string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
}

// hasHostSuffix reports whether host is one of the suffixes or a subdomain of
// one. The label boundary is checked explicitly: a bare strings.HasSuffix would
// match "notgoogle.com" against "google.com", which is exactly the kind of
// attacker-registrable near-miss a suffix list must not accept.
func hasHostSuffix(host string, suffixes []string) bool {
	for _, s := range suffixes {
		if host == s || strings.HasSuffix(host, "."+s) {
			return true
		}
	}
	return false
}

// Resolver is the subset of *net.Resolver this package needs, so tests inject a
// fake and never touch real DNS. Deliberately separate from dnsauth.Resolver
// rather than an extension of it: that package's doc commits it to email-auth
// verdicts from TXT records, and widening it to MX would make it two things.
type Resolver interface {
	LookupMX(ctx context.Context, name string) ([]*net.MX, error)
}

// NewResolver returns the production Resolver: Go's own, using the host's
// configured nameservers.
func NewResolver() Resolver { return net.DefaultResolver }

// Lookup classifies one recipient domain over DNS. ok is false when the lookup
// could not be completed, which the caller must treat as "no answer yet" and
// NOT persist — writing it would stamp checked_at and hide the domain from the
// next sweep for a full staleness window (the sending_domains convention,
// migration 000036).
//
// mxHost is the primary MX as observed, kept for operator diagnosis: when a
// domain reads Other, the host is the only thing that explains why.
func Lookup(ctx context.Context, res Resolver, domain string) (result ESP, mxHost string, ok bool) {
	if !resolvableName(domain) {
		return Unknown, "", false
	}
	ctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	records, err := res.LookupMX(ctx, domain)
	if err != nil {
		// A domain with no MX resolves as an error on most resolvers. That is a
		// completed answer, not a failure — the domain simply is not on Google or
		// Microsoft — so it is recorded as Other and not retried every sweep.
		//
		// This is why resolvableName above is load-bearing rather than tidy. Go's
		// resolver rejects a syntactically impossible name BEFORE sending a packet
		// and reports it as IsNotFound, indistinguishable here from a real "no such
		// domain". Recording that as Other would persist a verdict about a name
		// nobody ever asked DNS about — and, because the writer trims the key while
		// the sweep's fan-out does not, it would land on the TRIMMED domain's row.
		// A mailbox at "a@ example.com" would pin example.com to Other, hiding it
		// from re-lookup for a full staleness window and filing every warmup
		// observation to it under the wrong route, permanently, since observations
		// are immutable by design.
		if isNotFound(err) {
			return Other, "", true
		}
		return Unknown, "", false
	}
	hosts := make([]string, 0, len(records))
	for _, r := range records {
		if r == nil {
			continue
		}
		hosts = append(hosts, r.Host)
	}
	if len(hosts) == 0 {
		return Other, "", true
	}
	return FromMX(hosts), normalizeHost(hosts[0]), true
}

// resolvableName reports whether a string is worth asking a resolver about.
//
// Go's resolver rejects a syntactically impossible name locally and returns a
// DNSError with IsNotFound set — the same shape as a genuine NXDOMAIN — so
// without this check a malformed name becomes a persisted verdict of Other. A
// name that could never resolve is not evidence about anyone's DNS, and the
// esp package's own rule is that Other means "checked, and it is neither".
//
// Deliberately narrow: it rejects what cannot be a hostname (whitespace, empty
// labels, leading/trailing dots or hyphens, over-length) and does not attempt to
// be a full RFC 1035 validator. Anything it lets through and DNS rejects is
// still classified the same way it always was.
func resolvableName(domain string) bool {
	if domain == "" || len(domain) > maxDomainNameLength {
		return false
	}
	if strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") ||
		strings.HasPrefix(domain, "-") || strings.Contains(domain, "..") {
		return false
	}
	for i := 0; i < len(domain); i++ {
		switch c := domain[i]; {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

// maxDomainNameLength is RFC 1035's limit on a domain name.
const maxDomainNameLength = 253

// isNotFound reports whether err means "the name has no MX" rather than "we
// could not find out". Read from *net.DNSError.IsNotFound (which covers both
// NXDOMAIN and NODATA) and never from the error text, for the same reason
// dnsauth does it: a Go release or a different resolver could reword the string.
func isNotFound(err error) bool {
	var dnsErr *net.DNSError
	if !errors.As(err, &dnsErr) {
		return false
	}
	return dnsErr.IsNotFound
}
