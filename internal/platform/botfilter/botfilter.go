// Package botfilter decides whether a tracking-pixel hit or link click was
// made by a HUMAN or by a MACHINE (Apple Mail Privacy Protection, the Gmail
// image proxy, a corporate link scanner, a security appliance).
//
// Pure — no database, no clock, no network, no config — in the same spirit as
// platform/rotation, platform/cadence, platform/sendcap and platform/abtest.
// Every input is passed in, so the whole thing is table-testable and the API
// server and any future reporting backfill compute the identical verdict from
// the identical facts. A second implementation of this judgement is a bug: the
// open rate shown to an operator and the signal a conditional branch reads must
// agree.
//
// It runs on the PUBLIC tracking endpoint, which takes unauthenticated traffic
// at whatever volume a mail blast produces, so Classify does string and integer
// work only: no allocation-heavy parsing, no regexp, no I/O, and deliberately
// no external IP-intelligence lookup (see the Datacenter comment below).
//
// Nothing here is a security boundary. A tracking hit is entirely
// attacker-controllable — a User-Agent is a claim, not evidence, and a
// determined bot can present a plain browser string. The verdict is a
// REPORTING-QUALITY signal: it exists to stop honest infrastructure (which
// identifies itself accurately) from inflating an open rate or firing the wrong
// conditional branch. Treat a Human verdict as "not obviously a machine", never
// as proof a person was there.
package botfilter

import (
	"net/netip"
	"strings"
	"time"
)

// Verdict is the classification of a single tracking hit.
type Verdict string

const (
	// Human is the default: nothing about this hit looked machine-made.
	Human Verdict = "human"
	// Machine is a hit attributed to a proxy, scanner or prefetcher rather
	// than a person reading the message.
	Machine Verdict = "machine"
)

// Reason names WHY a hit was classified as Machine, so an operator can see
// which signal fired and a future rule change can be evaluated against stored
// history rather than guessed at. ReasonNone accompanies a Human verdict.
//
// These strings are PERSISTED (tracking_events.machine_reason), so an existing
// value must never be renamed or repurposed — add a new one instead. The column
// is plain TEXT rather than an enum for exactly that reason: adding a signal
// should not need a migration and a lock on a high-volume table.
type Reason string

const (
	// ReasonNone is the reason attached to a Human verdict.
	ReasonNone Reason = ""
	// ReasonProxyUserAgent: the User-Agent self-identifies as a known mail
	// proxy, scanner or security appliance.
	ReasonProxyUserAgent Reason = "proxy_user_agent"
	// ReasonPrefetchWindow: the open arrived so soon after the send that no
	// human could have read the message.
	ReasonPrefetchWindow Reason = "prefetch_window"
	// ReasonClickWithoutOpen: a click on a tracked message whose pixel never
	// fired, or fired at the same instant — a link scanner following every URL
	// in the message body.
	ReasonClickWithoutOpen Reason = "click_without_open"
	// ReasonDatacenterIP: the source address is in a range that cannot be a
	// residential or mobile reader.
	ReasonDatacenterIP Reason = "datacenter_ip"
	// ReasonBurstFromSubnet: many opens of ONE send from one /24 (or IPv6 /48)
	// in a short window — a scanning appliance walking a message.
	ReasonBurstFromSubnet Reason = "burst_from_subnet"
)

// Kind distinguishes the two tracked event types. It mirrors the
// tracking_event_kind enum without importing the persistence layer (a platform
// package depends on nothing above it).
type Kind string

// The tracked event kinds.
const (
	KindOpen  Kind = "open"
	KindClick Kind = "click"
)

// PrefetchWindow is how soon after a send an open is attributed to a machine.
//
// Two seconds is the value the previous read-time SQL filter already used, kept
// deliberately so this change re-homes that judgement without also moving the
// threshold — one behavioural change at a time. It is generous in the safe
// direction: a human cannot receive, notice and open a message inside two
// seconds, whereas a prefetch routinely fires in well under one, so the window
// costs no true opens.
const PrefetchWindow = 2 * time.Second

// BurstSubnetOpenThreshold is how many opens of ONE send from ONE /24 within
// BurstWindow mark the hit as a scanning appliance.
//
// A send goes to exactly one recipient, so a second address in the same /24
// opening it is already odd; five is well past coincidence while still leaving
// room for a mail client that retries the pixel a few times, and for a corporate
// NAT where several devices of the SAME person share an egress address.
const BurstSubnetOpenThreshold = 5

// BurstWindow bounds the burst count. The caller supplies only the prior events
// inside it; this constant names the window so the caller and the rule cannot
// drift apart.
const BurstWindow = 10 * time.Minute

// Hit is the one tracking event being classified. Every field is a fact the
// caller already has in hand at the tracking endpoint — assembling it must not
// require an extra query.
type Hit struct {
	// Kind is open or click.
	Kind Kind
	// UserAgent is the raw request User-Agent. It is an unverified claim.
	UserAgent string
	// IP is the resolved client address. The zero Addr (IsValid() == false)
	// means it could not be determined, which is treated as no signal rather
	// than as suspicious — an unknown address must not manufacture a verdict.
	IP netip.Addr
	// At is when the hit arrived.
	At time.Time
	// SentAt is when the send it belongs to went out. The zero Time means
	// unknown, which disables the timing rules rather than guessing.
	SentAt time.Time
}

// Prior is the small window of already-recorded history for the SAME send that
// the ordering and volume rules need. The caller reads it once per hit; it is a
// value, not a store, so this package stays pure.
type Prior struct {
	// HumanOpens is how many opens of this send are already recorded as Human.
	// A click is judged against human opens only: a scanner's own prefetch of
	// the pixel must not vouch for the click that follows it.
	HumanOpens int
	// LastHumanOpenAt is when the most recent Human open of this send arrived.
	// The zero Time means there was none.
	LastHumanOpenAt time.Time
	// OpensFromSubnet is how many opens of this send have already arrived from
	// the same /24 (IPv4) or /48 (IPv6) as this hit, within BurstWindow.
	OpensFromSubnet int
}

// Classify returns the verdict for one hit and the reason behind it.
//
// Signals are evaluated most-reliable first and the first match wins, so the
// stored reason names the strongest evidence rather than the last rule to run.
// Self-identification beats inference: a UA that says "I am the Gmail image
// proxy" is a stronger fact than any timing heuristic, and a datacenter address
// is a stronger fact than a burst count that a single retry could reach.
func Classify(hit Hit, prior Prior) (Verdict, Reason) {
	if isProxyUserAgent(hit.UserAgent) {
		return Machine, ReasonProxyUserAgent
	}
	if isDatacenterIP(hit.IP) {
		return Machine, ReasonDatacenterIP
	}
	switch hit.Kind {
	case KindOpen:
		return classifyOpen(hit, prior)
	case KindClick:
		return classifyClick(hit, prior)
	}
	// An unrecognized kind is not evidence of anything. Falling through to
	// Human keeps a future third event type reportable by default instead of
	// silently landing every one of its hits in the machine bucket.
	return Human, ReasonNone
}

// classifyOpen applies the open-only timing and volume rules.
func classifyOpen(hit Hit, prior Prior) (Verdict, Reason) {
	if withinPrefetchWindow(hit.SentAt, hit.At) {
		return Machine, ReasonPrefetchWindow
	}
	// The burst count excludes the current hit, so N prior opens plus this one
	// reaches the threshold at OpensFromSubnet == threshold-1.
	if prior.OpensFromSubnet+1 >= BurstSubnetOpenThreshold {
		return Machine, ReasonBurstFromSubnet
	}
	return Human, ReasonNone
}

// classifyClick applies the ordering rule: a real person opens a message before
// clicking a link inside it, because rendering the message is what fetches the
// pixel. A link scanner fetches the URLs without ever rendering, so its click
// arrives with no human open behind it — or in the same instant as one, when it
// walks the pixel and the links together.
//
// The rule is deliberately conditioned on a human open EXISTING. A recipient who
// blocks images entirely produces clicks with no open at all, and calling every
// one of those a machine would systematically discard the clicks of the most
// privacy-conscious readers — so an absent open is only damning when paired with
// a send time that says the click came too fast to be read.
func classifyClick(hit Hit, prior Prior) (Verdict, Reason) {
	if prior.HumanOpens == 0 {
		if withinPrefetchWindow(hit.SentAt, hit.At) {
			return Machine, ReasonClickWithoutOpen
		}
		return Human, ReasonNone
	}
	// An open exists, but the click landed in the same instant as it: nothing
	// was read in between. !After rather than Before so an identical timestamp
	// (the common case when a scanner fires both in one pass, and the only case
	// a second-resolution clock can even show) counts.
	if !hit.At.After(prior.LastHumanOpenAt) {
		return Machine, ReasonClickWithoutOpen
	}
	return Human, ReasonNone
}

// withinPrefetchWindow reports whether at falls inside PrefetchWindow of sentAt.
// A zero sentAt (unknown send time) is no signal, and a hit BEFORE the send is
// treated as no signal too: it means a clock skewed between the sender and the
// API, not that a machine fetched it, and inventing a verdict from a broken
// clock would misreport whole batches at once.
func withinPrefetchWindow(sentAt, at time.Time) bool {
	if sentAt.IsZero() || at.Before(sentAt) {
		return false
	}
	return at.Sub(sentAt) < PrefetchWindow
}

// proxyUserAgentMarkers is THE curated list of self-identifying mail proxies,
// image proxies, link scanners and security appliances. One place, on purpose:
// before this package the Gmail proxy check lived as a `NOT ILIKE
// '%GoogleImageProxy%'` literal copy-pasted into four separate SQL queries, so
// adding a second vendor meant editing four files and any miss silently skewed
// one report against the others.
//
// PROVENANCE. Each marker is a substring that appears in the User-Agent these
// products actually send, taken from the vendor's own published documentation
// or from the headers observed on live tracking traffic. They are matched
// case-insensitively as substrings because vendors version their UA strings
// freely ("GoogleImageProxy/1.0" today, a different suffix tomorrow) while the
// product token itself stays put.
//
// TO EXTEND. Add the smallest substring that is DISTINCTIVE to the product, and
// add a case to TestProxyUserAgentMarkersAreDistinctive in the test file with a
// real observed UA. Two rules matter more than completeness:
//
//  1. Never add a token that appears in an ordinary browser UA. "Mozilla",
//     "Safari", "Chrome", "Windows" and friends appear in nearly every real mail
//     client, so a marker containing one would classify every human as a
//     machine and silently zero out the open rate. The test enforces this
//     against a corpus of genuine client UAs.
//  2. Prefer under-matching. A missed proxy inflates the open rate, which is a
//     visible number an operator can question. A false positive DELETES a real
//     person's engagement from the headline rate and, once conditional branching
//     ships, sends them the wrong follow-up — a silent, unrecoverable error.
//
// Apple Mail Privacy Protection is the notable ABSENCE from this list. MPP
// proxies the fetch through Apple's relay but forwards the ORIGINAL client's
// User-Agent, so it is not detectable here at all; it is caught by the timing
// and datacenter-range rules instead. This is why the UA list alone is not the
// feature.
var proxyUserAgentMarkers = []string{
	// Google's image proxy, which fetches every image in a Gmail message on
	// receipt — before any human opens it. Documented by Google; the marker
	// that was already hardcoded in the SQL this package replaces.
	"googleimageproxy",
	// Microsoft Defender for Office 365 Safe Links, which follows every URL in
	// a message to detonate it. Fires on clicks, not opens.
	"safelinks",
	// Barracuda's email security gateway link protection.
	"barracuda",
	// Proofpoint's URL Defense / Targeted Attack Protection scanner.
	"proofpoint",
	// Mimecast's Attachment/URL Protect scanner.
	"mimecast",
	// Symantec/Broadcom Email Security.cloud click-time protection.
	"symantec",
	// Cisco Secure Email (formerly IronPort) outbreak filters.
	"ironport",
	// Generic self-identifying automation. These are conventional tokens a
	// well-behaved crawler or preview fetcher puts in its UA precisely so it
	// can be recognized; none appears in a real mail client's UA.
	"bot", "crawler", "spider", "preview", "scanner", "curl/", "wget/",
	"python-requests", "go-http-client", "libwww-perl", "java/",
	"headlesschrome", "phantomjs",
}

// isProxyUserAgent reports whether ua self-identifies as a known proxy or
// scanner. An EMPTY UA is deliberately NOT machine: plenty of legitimate mail
// clients and privacy-hardened readers strip the header, and treating absence
// as guilt would discard those readers entirely.
func isProxyUserAgent(ua string) bool {
	if ua == "" {
		return false
	}
	lower := strings.ToLower(ua)
	for _, marker := range proxyUserAgentMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// datacenterMarkers are address ranges that cannot host a residential or mobile
// mail reader, so a tracking hit from one is infrastructure by definition.
//
// SCOPE, and what is deliberately NOT here. This is the "cheaply determinable"
// half of the datacenter signal ONLY — ranges knowable from the address itself,
// with no lookup. There is intentionally NO external IP-intelligence dependency
// and NO network call: this runs on an unauthenticated endpoint at blast volume,
// where a per-hit lookup would add a network round trip to every pixel, make the
// endpoint's latency depend on a third party's uptime, and leak every
// recipient's IP to that third party. A shipped, cheap, partial signal beats a
// perfect one that cannot run here.
//
// Cloud-provider ranges (AWS/GCP/Azure publish machine-readable lists) are the
// obvious extension and are NOT included: they are hundreds of thousands of
// prefixes that change daily, so they belong in a periodically-refreshed table
// consulted off the hot path, not in a compiled-in slice that goes stale
// silently. Apple's MPP relay egress is the same story.
var datacenterMarkers = []netip.Prefix{
	// Public DNS / anycast resolvers and the scanners that share their ranges.
	// A mail client never fetches a pixel from one of these.
	netip.MustParsePrefix("8.8.8.0/24"),          // Google Public DNS
	netip.MustParsePrefix("8.8.4.0/24"),          // Google Public DNS
	netip.MustParsePrefix("1.1.1.0/24"),          // Cloudflare
	netip.MustParsePrefix("2001:4860:4860::/48"), // Google Public DNS v6
	netip.MustParsePrefix("2606:4700:4700::/48"), // Cloudflare v6
}

// isDatacenterIP reports whether addr is in a known infrastructure range.
//
// An INVALID address is not machine — an unresolvable client IP is missing data,
// and missing data must never manufacture a verdict.
//
// Loopback, private and link-local addresses are NOT machine either, even though
// no internet reader has one. Behind a misconfigured reverse proxy EVERY hit
// carries the proxy's private address, so calling those machine would zero out a
// whole deployment's open rate on a config mistake — exactly the silent,
// total-loss failure this package is built to avoid.
func isDatacenterIP(addr netip.Addr) bool {
	if !addr.IsValid() {
		return false
	}
	for _, p := range datacenterMarkers {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// SubnetKey groups an address into the block the burst rule counts over: a /24
// for IPv4, a /48 for IPv6 (the smallest block reliably assigned to one site).
// It returns the masked prefix and false for an invalid address, so a caller
// with no client IP skips the burst query rather than counting a bogus group.
func SubnetKey(addr netip.Addr) (netip.Prefix, bool) {
	if !addr.IsValid() {
		return netip.Prefix{}, false
	}
	bits := 24
	if addr.Is6() {
		bits = 48
	}
	p, err := addr.Prefix(bits)
	if err != nil {
		return netip.Prefix{}, false
	}
	return p, true
}
