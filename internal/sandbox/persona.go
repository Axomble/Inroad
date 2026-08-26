package sandbox

import (
	"fmt"
	"hash/fnv"
	"strings"
)

// Persona is one simulated prospect: the company they work at, who they are,
// and how they will behave when a campaign reaches them. Everything about a
// Persona is derived deterministically from its index, so two runs of the
// harness over the same seed produce byte-identical state — a demo screenshot
// stays reproducible, and a failing integration test can be re-run.
type Persona struct {
	FirstName string
	LastName  string
	Email     string
	Company   string
	Domain    string
	Title     string
	Behavior  Behavior
}

// FullName is the persona's display name, as a mail client would show it.
func (p Persona) FullName() string { return p.FirstName + " " + p.LastName }

// Behavior is what a persona does with a message that reaches them. The
// fields are independent gates applied in order: a persona that never opens
// never clicks, and only an opener can reply. Bounced is exclusive of all of
// them — a bounced address never received anything to act on.
type Behavior struct {
	Opens   bool
	Clicks  bool
	Replies bool
	Bounces bool
	// Reply is the flavour of reply this persona sends, meaningful only when
	// Replies is true. It maps onto the workspace's builtin reply-label keys
	// (migration 000047), so seeded threads exercise the real taxonomy rather
	// than inventing classes the UI has no label for.
	Reply ReplyFlavor
}

// ReplyFlavor is the kind of reply a persona writes. The zero value is
// deliberately the positive flavour so a Behavior built without one still
// produces a sensible thread rather than an empty body.
type ReplyFlavor int

const (
	ReplyPositive ReplyFlavor = iota
	ReplyQuestion
	ReplyNegative
	ReplyOutOfOffice
	ReplyUnsubscribe
)

// LabelKey maps a flavour onto the reply-label key the classifier would have
// assigned it. These are the builtin keys seeded into every workspace by the
// trigger in migration 000047; a flavour outside the known set resolves to
// "unknown", which is itself a seeded builtin, so a thread always has a label
// to join against.
func (f ReplyFlavor) LabelKey() string {
	switch f {
	case ReplyPositive:
		return "positive"
	case ReplyQuestion:
		// A question is engagement without a commitment either way, which is
		// exactly what the neutral label means to an operator triaging.
		return "neutral"
	case ReplyNegative:
		return "negative"
	case ReplyOutOfOffice:
		return "out_of_office"
	case ReplyUnsubscribe:
		return "unsubscribe"
	default:
		return "unknown"
	}
}

// Engagement rates, expressed in percent of the population. These are the
// believable-outcome targets the harness exists to produce; they are applied
// against a hash of the persona index, so the realised rate over a run of a
// few dozen personas is close to, not exactly, these numbers.
//
// The reply rate sits at the TOP of the plausible cold-outbound band (a good
// campaign runs 3-8%) rather than the middle, and the click and reply draws
// are conditioned on opening. That is a deliberate trade: the rates have to
// stay believable for the reporting screens, but they also have to yield
// enough threads that the inbox is worth opening. At 8% of openers, a
// DefaultContacts-sized run produces a couple of dozen threads across every
// reply label — see DefaultContacts, which is sized against this.
const (
	openRatePercent   = 40
	clickRatePercent  = 12
	replyRatePercent  = 8
	bounceRatePercent = 2
)

// Roster material. Names are drawn from several naming traditions and paired
// against plausible B2B company names, because the whole point of the harness
// is state a person can be shown without apologising for it: "test1@test.com"
// makes a demo look broken and makes a screenshot unusable.
var (
	firstNames = []string{
		"Amara", "Priya", "Tomas", "Ingrid", "Kenji", "Rosa", "Dimitri", "Fatima",
		"Callum", "Yuki", "Adaeze", "Mateo", "Saoirse", "Rahul", "Elena", "Bjorn",
		"Nadia", "Hugo", "Leila", "Anders", "Mei", "Ibrahim", "Clara", "Santiago",
	}
	lastNames = []string{
		"Okonkwo", "Lindqvist", "Moreau", "Haddad", "Nakamura", "Silva", "Petrov", "Rahman",
		"Doyle", "Tanaka", "Eze", "Vargas", "Byrne", "Iyer", "Novak", "Halvorsen",
		"Aziz", "Lefevre", "Karim", "Dahl", "Chen", "Farouk", "Weiss", "Duarte",
	}
	// Each company carries its own domain and the kind of business it is, so a
	// title can be plausible for the company rather than random.
	companies = []struct {
		Name   string
		Domain string
		Titles []string
	}{
		{"Northwind Logistics", "northwindlogistics.com", []string{"VP Operations", "Head of Fleet", "Director of Supply Chain"}},
		{"Lumen Analytics", "lumenanalytics.io", []string{"Head of Data", "VP Engineering", "Director of Analytics"}},
		{"Fernhill Health", "fernhillhealth.com", []string{"Chief Medical Officer", "Head of Clinical Ops", "VP Patient Experience"}},
		{"Sablepoint Capital", "sablepoint.partners", []string{"Managing Partner", "Head of Investor Relations", "Principal"}},
		{"Arcadia Robotics", "arcadiarobotics.dev", []string{"VP Hardware", "Head of Manufacturing", "Director of Product"}},
		{"Kestrel Payments", "kestrelpay.com", []string{"Head of Risk", "VP Compliance", "Director of Payments"}},
		{"Bluewater Studios", "bluewaterstudios.co", []string{"Executive Producer", "Head of Studio", "Creative Director"}},
		{"Ironvale Manufacturing", "ironvale.industries", []string{"Plant Director", "VP Operations", "Head of Procurement"}},
		{"Cedarline Retail", "cedarline.shop", []string{"Head of Ecommerce", "VP Merchandising", "Director of Growth"}},
		{"Halcyon Energy", "halcyonenergy.co", []string{"Head of Grid Strategy", "VP Development", "Director of Renewables"}},
	}
)

// BuildPersonas generates n personas. The roster is combinatorial rather than
// a fixed list: first name, surname and company advance on co-prime-ish
// strides so a run of 150 contacts does not repeat a (person, company) pair
// early, which is what makes the contacts screen look populated rather than
// looped.
func BuildPersonas(n int) []Persona {
	if n <= 0 {
		return nil
	}
	out := make([]Persona, 0, n)
	for i := range n {
		out = append(out, personaAt(i))
	}
	return out
}

// personaAt builds the persona at index i. Split out from BuildPersonas so it
// can be exercised directly for a single index in tests.
func personaAt(i int) Persona {
	first := firstNames[i%len(firstNames)]
	// Stride the surname and company by different amounts than the first name
	// so the three cycle out of phase and combinations stay fresh.
	last := lastNames[(i*7+i/len(firstNames))%len(lastNames)]
	co := companies[(i*3)%len(companies)]
	title := co.Titles[i%len(co.Titles)]

	return Persona{
		FirstName: first,
		LastName:  last,
		Email:     personaEmail(first, last, i, co.Domain),
		Company:   co.Name,
		Domain:    co.Domain,
		Title:     title,
		Behavior:  behaviorAt(i),
	}
}

// personaEmail builds a plausible corporate address. The index is folded in
// only when it has to be — after the first cycle of the surname table, where
// a (first, last, domain) triple could otherwise repeat — so most addresses
// read exactly like a real first.last@company one, and uniqueness (which
// contacts.email is uniquely indexed on, per workspace) still holds.
func personaEmail(first, last string, i int, domain string) string {
	local := strings.ToLower(first + "." + last)
	if cycle := i / len(lastNames); cycle > 0 {
		local = fmt.Sprintf("%s%d", local, cycle+1)
	}
	return local + "@" + domain
}

// behaviorAt assigns the engagement profile for index i. Each trait is drawn
// from an independently salted hash of the index rather than from i % k: a
// modulus makes traits line up in lockstep (every 8th contact opening AND
// clicking AND replying), which produces a funnel no real campaign ever has.
//
// The gates are nested deliberately — click and reply are conditioned on
// open, and a bounce excludes everything — because that is the causality the
// real pipeline has, and a seeded row that violates it would make the
// reporting screens show impossible ratios (more clicks than opens).
func behaviorAt(i int) Behavior {
	if percentile(i, "bounce") < bounceRatePercent {
		return Behavior{Bounces: true}
	}
	opens := percentile(i, "open") < openRatePercent
	if !opens {
		return Behavior{}
	}
	// Click and reply rates are stated as a share of the WHOLE population, but
	// only openers reach this point — so each draw is rescaled by the open
	// rate to keep the realised population-wide rate equal to the stated one.
	// Comparing the raw percentile here instead would silently deliver
	// rate*openRate (a stated 8% reply rate arriving as 3%), which is how the
	// first cut of this seeded an inbox with two threads in it.
	b := Behavior{
		Opens:  true,
		Clicks: percentile(i, "click") < scaleByOpenRate(clickRatePercent),
		// repliesAt re-derives the same gates this function already passed,
		// which is redundant here but keeps ONE definition of "replies" that
		// flavorAt's dealing can also consult.
		Replies: repliesAt(i),
	}
	if b.Replies {
		b.Reply = flavorAt(i)
	}
	return b
}

// scaleByOpenRate converts a population-wide target rate into the threshold to
// apply among openers, so that (openRate * threshold) lands back on the target.
// Capped at 100, since a target above the open rate cannot be reached by a
// trait that only openers can have.
func scaleByOpenRate(targetPercent int) int {
	scaled := targetPercent * 100 / openRatePercent
	if scaled > 100 {
		return 100
	}
	return scaled
}

// flavorDeal is the order flavours are dealt to repliers. Positive appears
// most often, because that is the outcome an operator most wants the product
// to surface, but every flavour appears within the first cycle.
var flavorDeal = []ReplyFlavor{
	ReplyPositive, ReplyQuestion, ReplyNegative, ReplyPositive,
	ReplyOutOfOffice, ReplyQuestion, ReplyUnsubscribe, ReplyPositive,
	ReplyNegative, ReplyOutOfOffice,
}

// flavorAt assigns a reply flavour by DEALING from flavorDeal in replier
// order, rather than drawing another hash.
//
// This is the one place the harness deliberately abandons independent random
// draws, because independence is not what is wanted here. A run produces only
// a couple of dozen repliers, and at that sample size an honest weighted draw
// leaves whole triage buckets empty maybe half the time — the unsubscribe
// label was empty at a 16% weight — which makes the inbox demo show three of
// five labels with nothing behind them and makes the test asserting otherwise
// flaky for a reason no code change would fix.
//
// Dealing guarantees coverage within the first ten repliers while staying
// fully deterministic. The cost is that flavour no longer correlates with
// anything about the persona, which costs nothing: it never did.
func flavorAt(i int) ReplyFlavor {
	return flavorDeal[replierRank(i)%len(flavorDeal)]
}

// replierRank counts how many personas before index i also reply, giving each
// replier a stable position in the deal. O(i) per call and only ever run over
// a seeding population, which is small; a shared counter would be faster but
// would make behaviorAt depend on the order it was called in, and a pure
// function of the index is worth more here than the microseconds.
func replierRank(i int) int {
	rank := 0
	for j := range i {
		if repliesAt(j) {
			rank++
		}
	}
	return rank
}

// repliesAt reports whether persona j replies, WITHOUT consulting the flavour
// (which is what replierRank is computing) — the gate behaviorAt applies,
// factored out so the two can never disagree.
func repliesAt(j int) bool {
	if percentile(j, "bounce") < bounceRatePercent {
		return false
	}
	if percentile(j, "open") >= openRatePercent {
		return false
	}
	return percentile(j, "reply") < scaleByOpenRate(replyRatePercent)
}

// percentile hashes (i, salt) into 0..99. The salt is what makes the traits
// independent: the same index yields uncorrelated draws for "open" and
// "reply", so the funnel does not collapse onto one modulus.
//
// The result is taken from the HIGH bits (via a final avalanche) rather than
// as a bare sum % 100. FNV-1a's low bits over short, similar inputs — which
// "open:41" and "reply:41" very much are — stay correlated, and a plain
// modulus reads exactly those bits: the first cut of this produced a reply
// population that was two thirds one flavour and never once another, because
// the reply draw and the flavour draw were effectively the same number.
func percentile(i int, salt string) int {
	h := fnv.New32a()
	// Hash writes never fail (hash.Hash's Write always returns a nil error),
	// so there is nothing to handle here.
	fmt.Fprintf(h, "%s:%d", salt, i)
	return int(avalanche(h.Sum32()) % 100)
}

// avalanche is the finalizer from MurmurHash3, which mixes every input bit
// into every output bit. Applied to FNV's output it breaks the low-bit
// correlation percentile's modulus would otherwise inherit.
func avalanche(x uint32) uint32 {
	x ^= x >> 16
	x *= 0x85ebca6b
	x ^= x >> 13
	x *= 0xc2b2ae35
	x ^= x >> 16
	return x
}
