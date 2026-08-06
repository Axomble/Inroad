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
	if domain == "" {
		return Unknown, "", false
	}
	ctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	records, err := res.LookupMX(ctx, domain)
	if err != nil {
		// A domain with no MX resolves as an error on most resolvers. That is a
		// completed answer, not a failure — the domain simply is not on Google or
		// Microsoft — so it is recorded as Other and not retried every sweep.
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
