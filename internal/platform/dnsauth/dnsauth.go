// Package dnsauth checks whether a sending domain publishes the email
// authentication records the bulk-sender rules require: SPF at the apex, DMARC
// at _dmarc, and (advisory only) a DKIM key on one of a set of probed selectors.
//
// Nothing here touches Postgres or the clock, and the only network access is
// through the injected Resolver, so the whole package is table-testable without
// DNS. Two judgements shape the API and must survive any refactor:
//
//  1. DKIM never decides the verdict. Selectors are NOT discoverable from DNS —
//     you can only guess common ones — so "not found" means "none of my guesses
//     matched". Letting that fail a domain would tell an operator whose mail is
//     correctly signed on an unusual selector that they are broken. Passing
//     requires SPF AND DMARC only.
//  2. A transient lookup failure is Unknown, never Failing. A timeout or SERVFAIL
//     means "could not check", and rendering it as "your domain is misconfigured"
//     sends people editing DNS that was already correct. NXDOMAIN is the
//     exception — it is a real negative — so the two are distinguished by the
//     resolver's own IsNotFound flag rather than by matching error strings.
package dnsauth

import (
	"context"
	"errors"
	"net"
	"strings"
	"time"
)

// lookupTimeout bounds a single TXT lookup. Short on purpose: this runs on an
// operator-facing request (the on-demand recheck) and in a sweep over every
// domain, and a resolver that has not answered in this long is reported as
// Unknown rather than held onto.
const lookupTimeout = 5 * time.Second

// dkimProbeBudget bounds ALL the selector probes together. Nine selectors at the
// per-lookup timeout would be 45 seconds of an operator's recheck spent on the
// one part of the check that cannot change the answer, so the probe gives up as
// a whole and reports "not detected" — which is what an unmatched selector
// already means.
const dkimProbeBudget = 10 * time.Second

// State is the verdict for a domain. It is a string because it is persisted and
// serialized verbatim (sending_domains.state, the API's DomainAuthState enum),
// so there is one spelling of each value in the system rather than a mapping
// table per boundary.
type State string

const (
	// StateUnknown means the check could not be completed — a lookup failed for
	// a reason other than "the record does not exist". Never a failure.
	StateUnknown State = "unknown"
	// StatePassing means SPF and DMARC are both published. DKIM is not consulted.
	StatePassing State = "passing"
	// StateFailing means at least one of SPF/DMARC is genuinely absent.
	StateFailing State = "failing"
)

// Resolver is the subset of *net.Resolver this package needs, so tests inject a
// fake and never touch real DNS.
type Resolver interface {
	LookupTXT(ctx context.Context, name string) ([]string, error)
}

// NewResolver returns the production Resolver: Go's own, using the host's
// configured nameservers.
func NewResolver() Resolver { return net.DefaultResolver }

// SPF is the sender-policy record at the domain apex.
type SPF struct {
	Found bool
	Value string
}

// DMARC is the policy record at _dmarc.<domain>. Policy is the p= tag
// ("none" | "quarantine" | "reject", or "" when absent/unparseable): p=none is
// monitoring, not enforcement, and an operator should see the difference.
type DMARC struct {
	Found  bool
	Value  string
	Policy string
}

// DKIM is whichever probed selector answered first. Advisory only (see the
// package doc): Selector names the guess that matched, and an empty DKIM means
// "none of the probed selectors matched", not "unsigned".
type DKIM struct {
	Found    bool
	Value    string
	Selector string
}

// Result is one domain's check. LookupError records that an AUTHORITATIVE lookup
// (SPF or DMARC) failed for a non-NXDOMAIN reason; a failed DKIM probe never
// sets it, because DKIM cannot decide the verdict either way.
type Result struct {
	Domain      string
	SPF         SPF
	DMARC       DMARC
	DKIM        DKIM
	LookupError bool
}

// State reduces a Result to its verdict. Unknown wins over everything (we did
// not get an answer, so we have nothing to say), and DKIM is deliberately not
// read here at all.
func (r Result) State() State {
	switch {
	case r.LookupError:
		return StateUnknown
	case r.SPF.Found && r.DMARC.Found:
		return StatePassing
	default:
		return StateFailing
	}
}

// DefaultSelectors are the DKIM selectors worth guessing: the ones Google
// Workspace, Microsoft 365, and the common ESPs publish. It returns a fresh
// slice rather than exposing a package-level variable a caller could mutate.
//
// The list is a heuristic and is expected to miss custom selectors — which is
// exactly why a miss is advisory (package doc, judgement 1).
func DefaultSelectors() []string {
	return []string{"google", "default", "selector1", "selector2", "k1", "mail", "dkim", "s1", "s2"}
}

// Check performs the three lookups for one domain. selectors extends (does not
// replace) the default DKIM probe set with any operator-supplied selector;
// passing nil probes the defaults alone. It never returns an error: every
// failure mode is expressed in the Result, because "could not check" is a state
// the caller has to render, not an exception.
func Check(ctx context.Context, res Resolver, domain string, selectors []string) Result {
	out := Result{Domain: Normalize(domain)}
	if out.Domain == "" {
		// Nothing to look up. Reported as Unknown (via LookupError) rather than
		// as a failing domain — we learned nothing about anyone's DNS.
		out.LookupError = true
		return out
	}

	spfTXT, err := lookup(ctx, res, out.Domain)
	if isLookupError(err) {
		out.LookupError = true
	}
	if v, ok := firstWithPrefix(spfTXT, "v=spf1"); ok {
		out.SPF = SPF{Found: true, Value: v}
	}

	dmarcTXT, err := lookup(ctx, res, "_dmarc."+out.Domain)
	if isLookupError(err) {
		out.LookupError = true
	}
	if v, ok := firstWithPrefix(dmarcTXT, "v=DMARC1"); ok {
		out.DMARC = DMARC{Found: true, Value: v, Policy: dmarcPolicy(v)}
	}

	out.DKIM = probeDKIM(ctx, res, out.Domain, selectors)
	return out
}

// Normalize puts a domain in the one form everything else uses: lowercase, no
// surrounding space, no trailing root dot. The persisted domain, the API path
// parameter, and the name handed to the resolver all pass through here, so
// "ACME.com." and "acme.com" can never become two rows or two lookups.
//
// A string that cannot be a hostname normalizes to "", which every caller
// already treats as "not a domain this workspace sends from". That is stricter
// than it looks: a path parameter carrying a control character (a %00 is the easy
// one) used to reach Postgres, which rejects it as an invalid text literal — an
// error class that is neither ErrNoRows nor anything the handler maps, so a
// caller could turn bad input into a 500. Rejecting it at the one place every
// caller passes through closes that for all of them, rather than teaching each
// store method to recognise a malformed-input error from the driver.
func Normalize(domain string) string {
	d := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
	if d == "" || !isHostnameShaped(d) {
		return ""
	}
	return d
}

// maxDomainLength is the DNS limit on a presentation-format name (RFC 1035 §2.3.4).
const maxDomainLength = 253

// isHostnameShaped reports whether s could be a DNS name at all. It is a shape
// check, not a validity check — resolving is what decides whether a well-formed
// name exists. Anything outside printable ASCII is rejected: an internationalised
// domain reaches us already punycoded, so a raw non-ASCII byte here is malformed
// input rather than a legitimate name we would be refusing.
func isHostnameShaped(s string) bool {
	if len(s) > maxDomainLength {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '.' || r == '-' || r == '_':
		default:
			return false
		}
	}
	return true
}

// probeDKIM walks the selectors in order and stops at the first hit — the point
// is "is this domain signing at all", so continuing after a match would be extra
// DNS traffic for no extra information. Lookup errors are swallowed on purpose:
// a DKIM probe can never make a domain Unknown or Failing.
func probeDKIM(ctx context.Context, res Resolver, domain string, extra []string) DKIM {
	ctx, cancel := context.WithTimeout(ctx, dkimProbeBudget)
	defer cancel()
	for _, sel := range append(DefaultSelectors(), extra...) {
		sel = strings.TrimSpace(sel)
		if sel == "" {
			continue
		}
		txt, err := lookup(ctx, res, sel+"._domainkey."+domain)
		if err != nil {
			continue
		}
		for _, rec := range txt {
			if isDKIM(rec) {
				return DKIM{Found: true, Value: strings.TrimSpace(rec), Selector: sel}
			}
		}
	}
	return DKIM{}
}

// lookup runs one TXT query under its own timeout so a single unresponsive name
// cannot stall the whole check.
func lookup(ctx context.Context, res Resolver, name string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	return res.LookupTXT(ctx, name)
}

// isLookupError reports whether err means "we could not find out" as opposed to
// "the name does not exist". NXDOMAIN (and NODATA, which Go reports the same
// way) is a real negative and yields Failing; anything else — timeout, SERVFAIL,
// refused, a cancelled context — yields Unknown.
//
// The distinction is read from *net.DNSError.IsNotFound, never from the error
// text: invariant 1 is too important to rest on a string match that a Go release
// or a different resolver could reword.
func isLookupError(err error) bool {
	if err == nil {
		return false
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return !dnsErr.IsNotFound
	}
	return true
}

// firstWithPrefix returns the first TXT record opening with the version tag. A
// name legitimately carries many TXT records — verification tokens, other
// vendors' keys — so anything that is not the record we asked about is skipped
// rather than treated as malformed.
//
// The tag must be followed by a separator or the end of the record: "v=spf10"
// and "v=DMARC2" are different (future or bogus) versions, and matching them on
// a bare prefix would report a record this checker cannot actually read.
func firstWithPrefix(records []string, tag string) (string, bool) {
	for _, rec := range records {
		trimmed := strings.TrimSpace(rec)
		rest, ok := strings.CutPrefix(strings.ToLower(trimmed), strings.ToLower(tag))
		if !ok {
			continue
		}
		if rest == "" || strings.ContainsAny(rest[:1], " \t;") {
			return trimmed, true
		}
	}
	return "", false
}

// dmarcPolicy extracts the p= tag from a DMARC record. Tags are unordered, so it
// scans them all rather than assuming p= follows v=; an absent, empty, or
// unrecognized policy yields "" (the record is still Found — a published DMARC
// record with an unreadable policy is not a missing record).
//
// sp= (the subdomain policy) is deliberately ignored: this feature reports on
// the domain the mail is sent from, not its children.
func dmarcPolicy(record string) string {
	for _, part := range strings.Split(record, ";") {
		key, value, ok := strings.Cut(part, "=")
		if !ok || strings.ToLower(strings.TrimSpace(key)) != "p" {
			continue
		}
		switch policy := strings.ToLower(strings.TrimSpace(value)); policy {
		case "none", "quarantine", "reject":
			return policy
		default:
			return ""
		}
	}
	return ""
}

// isDKIM reports whether a TXT record at a _domainkey name is a DKIM key. The
// v=DKIM1 tag is RECOMMENDED, not required (RFC 6376 §3.6.1), and plenty of live
// records start straight at k=/p=, so a public-key tag counts as a match too —
// being strict here would under-report signing on exactly the domains this
// probe is least sure about.
func isDKIM(record string) bool {
	trimmed := strings.ToLower(strings.TrimSpace(record))
	if strings.HasPrefix(trimmed, "v=dkim1") {
		return true
	}
	for _, part := range strings.Split(trimmed, ";") {
		key, value, ok := strings.Cut(part, "=")
		if ok && strings.TrimSpace(key) == "p" && strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}
