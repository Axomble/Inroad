package warmup

import (
	"strings"

	"golang.org/x/net/publicsuffix"
)

// Where "which mailboxes share a reputation" is decided — ONE implementation, in
// Go, evaluated at read time.
//
// The campaign gate is mailbox AND domain (design §7), and "domain" means the
// ORGANIZATIONAL domain: providers largely inherit reputation across subdomains,
// so a quarantined a@example.com has almost certainly damaged the standing of
// b@mail.example.com. The gate used to group on the exact host
// (lower(split_part(email,'@',2))), which is narrower than the scope the design
// claims and than what the providers actually do.
//
// Three places could own the derivation. This one was chosen:
//
//   - SQL cannot: Postgres has no public-suffix data, and shipping a copy of the
//     list as a table would be a second implementation of the very question this
//     comment exists to keep singular.
//   - A stored column on mailboxes, written at connect time, would let SQL keep
//     grouping — but the public suffix list is DATA THAT MOVES. A stored value
//     freezes the answer at the library version in force when the mailbox was
//     connected, so two mailboxes connected either side of an x/net upgrade would
//     sit in different groups while claiming the same rule, and containment would
//     silently under-apply. It also cannot be backfilled: a SQL-only migration has
//     no public-suffix data, so existing rows would carry the exact host while new
//     rows carried the eTLD+1 — one column, two derivations, disagreeing.
//   - Go at read time (here) has one implementation, needs no backfill, is correct
//     for rows written by any path, and follows the list as it is upgraded. The
//     cost is that the grouping query cannot do the grouping: callers read the
//     workspace's participant lanes and fold them here instead.
//
// This is deliberately NOT the same question as `sending_domains` or
// `esp.Domain`, which key on the exact host. Those ask "which DNS name is this",
// for a record lookup and an MX classification; SPF/DKIM/DMARC and MX are
// published per host, so the exact host is the right answer there. This asks
// "whose reputation does this spend", which is broader by design.
//
// CONSUMER PROVIDERS ARE NOT A SHARED-REPUTATION UNIT. The gate exists because a
// provider that penalises acme.com penalises every mailbox on it — the workspace
// controls the domain, so its mailboxes genuinely share standing. That is simply
// untrue of gmail.com: Google does not penalise alice@gmail.com because
// bob@gmail.com sent spam, and the two are usually different customers entirely.
//
// eTLD+1 does not distinguish them (gmail.com is its own registrable domain), and
// no DNS signal does either — consumer providers publish SPF and DMARC just as a
// custom domain does. So the distinction has to be named, and it is named below.
//
// An earlier note here argued against any list on the grounds that a list would be
// wrong and unmaintained. That is right about a LARGE list and wrong about this
// one: the set of consumer mail domains a cold-email tool actually meets is a
// dozen names, they do not churn, and being absent from it fails in the CURRENT
// direction (treat as shared), so an omission costs over-containment rather than
// under-containment. Cheap Gmail mailboxes are common in cold email, so this is a
// live case, not a hypothetical.

// consumerMailDomains are registrable domains where a shared domain does NOT imply
// shared sender reputation, because the tenants are unrelated strangers rather than
// one workspace's mailboxes. Deliberately minimal: only domains whose whole purpose
// is per-person consumer mailboxes. A business on Google Workspace or M365 sends
// from its OWN domain, which is not here and is correctly gated as shared.
//
// Missing an entry means that domain is treated as shared — over-containment, the
// same behaviour as before this list existed. That is the safe direction for an
// omission, which is why the list can afford to be short.
var consumerMailDomains = map[string]bool{
	"gmail.com": true, "googlemail.com": true,
	"outlook.com": true, "hotmail.com": true, "live.com": true, "msn.com": true,
	"yahoo.com": true, "ymail.com": true, "aol.com": true,
	"icloud.com": true, "me.com": true, "mac.com": true,
	"proton.me": true, "protonmail.com": true,
	"gmx.com": true, "gmx.net": true, "mail.com": true, "zoho.com": true,
	"yandex.com": true, "yandex.ru": true, "qq.com": true, "163.com": true,
}

// SharesDomainReputation reports whether mailboxes on this address's organizational
// domain plausibly share sender standing, and therefore whether one being contained
// should withhold its siblings.
func SharesDomainReputation(email string) bool {
	domain := OrganizationalDomain(email)
	return domain != "" && !consumerMailDomains[domain]
}

// OrganizationalDomain returns the registrable domain (eTLD+1) of an email
// address, lower-cased: the unit the gate groups mailboxes by. Empty when the
// address carries no domain at all.
//
// A host the public suffix list cannot resolve — a bare label like `localhost`,
// a public suffix used as a domain (`co.uk`), an IP literal — falls back to the
// exact host. That is the pre-eTLD+1 grouping: narrower than intended, never
// wider, and never a bucket that lumps unrelated hosts together.
func OrganizationalDomain(email string) string {
	host := addressHost(email)
	if host == "" {
		return ""
	}
	registrable, err := publicsuffix.EffectiveTLDPlusOne(host)
	if err != nil {
		// Not an error worth propagating: every caller's only sane response
		// would be this fallback, and a gate that refuses to answer stops
		// containing anything.
		return host
	}
	return registrable
}

// addressHost is the lower-cased host part of an email address.
//
// It splits on the FIRST '@', which is what the SQL this replaces computed
// (split_part(email,'@',2)) — so the two can never disagree about an address
// either could see, including a malformed "a@b@c.example" that is undeliverable
// anyway. It deliberately does not reuse esp.Domain, which computes the same
// thing for a different reason (the recipient-ESP cache key, pinned to the SQL
// that fills that cache): sharing it would tie the reputation grouping to a
// change made for the cache.
func addressHost(email string) string {
	_, host, ok := strings.Cut(email, "@")
	if !ok || host == "" {
		return ""
	}
	return strings.ToLower(host)
}

// MailboxLane pairs one warmup participant's address with its live pool lane —
// the two columns the fold below needs, and no more, so the callers' generated
// row types stay out of this package.
type MailboxLane struct {
	Email string
	Lane  string
}

// DomainLanes is the worst lane on each organizational domain in a workspace.
// Built once per read and queried per mailbox, so the "what domain is this"
// question is asked in exactly one place on both sides of the comparison.
type DomainLanes map[string]string

// WorstLanesByDomain folds a workspace's ENABLED participants into the worst lane
// per organizational domain. Enabled-only is the caller's job (a disabled
// participant's lane is frozen history, not a live signal).
//
// Addresses with no domain are skipped rather than grouped under "": an
// ungroupable mailbox must not become a bucket that contains every other
// ungroupable mailbox.
func WorstLanesByDomain(participants []MailboxLane) DomainLanes {
	out := make(DomainLanes, len(participants))
	for _, p := range participants {
		// A consumer-provider mailbox contributes nothing to a domain verdict: its
		// neighbours are strangers, not this workspace's other senders.
		if !SharesDomainReputation(p.Email) {
			continue
		}
		domain := OrganizationalDomain(p.Email)
		if current, ok := out[domain]; !ok || laneSeverity(p.Lane) > laneSeverity(current) {
			out[domain] = p.Lane
		}
	}
	return out
}

// For returns the worst lane on this address's organizational domain, or "" when
// no participant is warming up there — which every lane predicate already reads
// as "not gated".
func (d DomainLanes) For(email string) string {
	// Symmetric with the fold: a consumer-provider mailbox is never withheld by a
	// domain verdict, because it never contributed to one. Checked on BOTH sides so
	// the map cannot be read with a different rule than it was built with — the
	// "two things that must agree" shape this subsystem keeps producing.
	if !SharesDomainReputation(email) {
		return ""
	}
	return d[OrganizationalDomain(email)]
}

// laneSeverity orders lanes by how contained they are, so "worst on the domain"
// is well defined. It is an ORDERING only: which lanes actually withhold new
// leads is NewLeadsWithheld's decision, and this must not become a second
// opinion about that. Unknown values rank lowest — they cannot be produced (the
// lane column is CHECK-constrained) and inventing severity for one would be
// containment nobody chose.
func laneSeverity(lane string) int {
	switch lane {
	case LaneBlocked:
		return 6
	case LaneQuarantine:
		return 5
	case LanePendingAuth:
		return 4
	case LaneRecovery:
		return 3
	case LaneProbation:
		return 2
	case LaneWatch:
		return 1
	default: // healthy, and anything unrecognised
		return 0
	}
}
