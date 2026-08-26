package warmup

import "testing"

func TestOrganizationalDomainGroupsSubdomainsWithTheirParent(t *testing.T) {
	cases := []struct {
		email, want string
	}{
		{"a@example.com", "example.com"},
		// The whole point: providers largely inherit reputation across
		// subdomains, so these two mailboxes share a standing.
		{"b@mail.example.com", "example.com"},
		{"c@a.b.example.co.uk", "example.co.uk"},
		// A multi-label public suffix is NOT a shared domain: example.co.uk and
		// other.co.uk belong to unrelated organisations.
		{"d@other.co.uk", "other.co.uk"},
		{"E@MAIL.Example.COM", "example.com"},
		// Hosts the public suffix list cannot resolve fall back to the exact
		// host, which is the pre-eTLD+1 behaviour: narrower, never wider.
		{"e@localhost", "localhost"},
		{"f@co.uk", "co.uk"},
		{"g@192.168.0.1", "192.168.0.1"},
		// No domain to group on at all.
		{"nobody", ""},
		{"trailing@", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := OrganizationalDomain(tc.email); got != tc.want {
			t.Errorf("OrganizationalDomain(%q) = %q, want %q", tc.email, got, tc.want)
		}
	}
}

// The gate is "the WORST lane on this mailbox's organizational domain", so the
// fold has to rank, not merely detect — a domain with a quarantined mailbox and a
// healthy one is quarantined, whichever order the rows arrive in.
func TestWorstLanesByDomainRanksAndGroupsAcrossSubdomains(t *testing.T) {
	lanes := WorstLanesByDomain([]MailboxLane{
		{Email: "healthy@example.com", Lane: LaneHealthy},
		{Email: "contained@mail.example.com", Lane: LaneQuarantine},
		{Email: "watched@shop.example.com", Lane: LaneWatch},
		{Email: "clean@other.test", Lane: LaneHealthy},
		{Email: "blocked@deep.other.test", Lane: LaneBlocked},
	})

	// Every one of these addresses is on example.com, subdomains included.
	for _, email := range []string{"healthy@example.com", "watched@shop.example.com", "new@promo.example.com"} {
		if got := lanes.For(email); got != LaneQuarantine {
			t.Errorf("For(%q) = %q, want quarantine — the worst lane on the organizational domain", email, got)
		}
	}
	if got := lanes.For("clean@other.test"); got != LaneBlocked {
		t.Errorf("For(clean@other.test) = %q, want blocked", got)
	}
	// A domain nobody is warming up on has no lane, which every lane predicate
	// already reads as "not gated".
	if got := lanes.For("someone@unrelated.test"); got != "" {
		t.Errorf("For(someone@unrelated.test) = %q, want empty", got)
	}
	if got := lanes.For("malformed"); got != "" {
		t.Errorf("For(malformed) = %q, want empty", got)
	}
}

// Ranking must be independent of row order: the same set arriving worst-first
// must produce the same verdict as best-first.
func TestWorstLanesByDomainIsOrderIndependent(t *testing.T) {
	worstFirst := WorstLanesByDomain([]MailboxLane{
		{Email: "a@example.com", Lane: LaneQuarantine},
		{Email: "b@example.com", Lane: LaneHealthy},
	})
	bestFirst := WorstLanesByDomain([]MailboxLane{
		{Email: "b@example.com", Lane: LaneHealthy},
		{Email: "a@example.com", Lane: LaneQuarantine},
	})
	if worstFirst.For("c@example.com") != bestFirst.For("c@example.com") {
		t.Fatalf("order changed the verdict: %q vs %q",
			worstFirst.For("c@example.com"), bestFirst.For("c@example.com"))
	}
	if got := bestFirst.For("c@example.com"); got != LaneQuarantine {
		t.Fatalf("For = %q, want quarantine", got)
	}
}

// A mailbox with no address cannot be grouped, and must not become a bucket that
// silently contains every other ungroupable mailbox.
func TestWorstLanesByDomainIgnoresAddressesWithNoDomain(t *testing.T) {
	lanes := WorstLanesByDomain([]MailboxLane{{Email: "broken", Lane: LaneQuarantine}})
	if len(lanes) != 0 {
		t.Fatalf("lanes = %v, want none: an address with no domain groups nothing", lanes)
	}
}

// The gate exists because a provider penalising acme.com penalises every mailbox on
// it — the workspace controls the domain, so its mailboxes share standing. That is
// untrue of gmail.com: Google does not penalise alice@gmail.com for bob@gmail.com's
// spam, and cheap consumer mailboxes are common in cold email, so treating them as
// one reputation unit stops campaigns for mailboxes that are genuinely unaffected.
func TestConsumerProviderMailboxesDoNotShareDomainReputation(t *testing.T) {
	for _, email := range []string{
		"alice@gmail.com", "bob@outlook.com", "c@yahoo.com", "d@icloud.com", "e@proton.me",
	} {
		if SharesDomainReputation(email) {
			t.Errorf("%s: a consumer mailbox must not share a domain verdict with strangers", email)
		}
	}
	// A business on Google Workspace sends from its OWN domain, which IS shared.
	for _, email := range []string{"ops@acme.com", "sales@mail.acme.co.uk"} {
		if !SharesDomainReputation(email) {
			t.Errorf("%s: a workspace-controlled domain must still be gated as shared", email)
		}
	}
}

// Both sides of the map must apply the same rule. Building it with one definition of
// "shares a domain" and reading it with another is the defect shape this subsystem
// has produced repeatedly.
func TestAConsumerMailboxIsNeitherCountedNorWithheld(t *testing.T) {
	lanes := WorstLanesByDomain([]MailboxLane{
		{Email: "quarantined@gmail.com", Lane: LaneQuarantine},
		{Email: "healthy@gmail.com", Lane: LaneHealthy},
		{Email: "quarantined@acme.com", Lane: LaneQuarantine},
		{Email: "healthy@acme.com", Lane: LaneHealthy},
	})

	// It did not contribute...
	if got := lanes["gmail.com"]; got != "" {
		t.Errorf("gmail.com acquired a domain verdict %q from unrelated tenants", got)
	}
	// ...and it is not withheld by one.
	if got := lanes.For("healthy@gmail.com"); got != "" {
		t.Errorf("a consumer mailbox was withheld by lane %q it cannot share", got)
	}
	// The custom domain still behaves exactly as before: worst lane wins.
	if got := lanes.For("healthy@acme.com"); got != LaneQuarantine {
		t.Errorf("acme.com domain lane = %q, want quarantine — a real shared domain must still gate", got)
	}
}

// The KEY half of the gate, on its own — and the proof that there is only one of it.
//
// A caller that GROUPS senders by shared fault (the rotation exposure budget) needs
// the key, then needs that group's verdict. Deriving the key at the call site and
// indexing the map would be a second expression of the rule the fold applies, and
// the two would agree only by inspection. This asserts they are the same value for
// every address shape the fold treats differently.
func TestSharedReputationDomainIsTheOneKeyTheFoldAndTheLookupUse(t *testing.T) {
	participants := []MailboxLane{
		{Email: "ops@mail.acme.co.uk", Lane: LaneWatch},
		{Email: "alice@gmail.com", Lane: LaneQuarantine},
		{Email: "nodomain", Lane: LaneBlocked},
	}
	lanes := WorstLanesByDomain(participants)

	for _, p := range participants {
		key := SharedReputationDomain(p.Email)
		if got, want := lanes.ForDomain(key), lanes.For(p.Email); got != want {
			t.Errorf("%s: ForDomain(%q) = %q but For(email) = %q — one key, or the two will drift",
				p.Email, key, got, want)
		}
	}
	if got := SharedReputationDomain("ops@mail.acme.co.uk"); got != "acme.co.uk" {
		t.Errorf("shared domain = %q, want acme.co.uk — the eTLD+1 the fold groups on", got)
	}
	// Both addresses the fold SKIPS resolve to no key at all. "" must never become a
	// bucket that lumps a consumer mailbox together with an ungroupable one.
	for _, email := range []string{"alice@gmail.com", "nodomain"} {
		if got := SharedReputationDomain(email); got != "" {
			t.Errorf("%s: shared domain = %q, want none", email, got)
		}
	}
	if len(lanes) != 1 {
		t.Errorf("lanes = %v, want just the one genuinely shared domain", lanes)
	}
}

// A DomainLanes with an empty key must not become a verdict on every mailbox the
// carve-out excludes.
//
// The fold never stores "", so this cannot happen today — which is exactly why the
// guard in For is worth having rather than resting on that. DomainLanes is an
// exported map type, so a future caller can build one directly, and if it ever
// carried an "" key then every consumer-provider and malformed-address mailbox would
// inherit that lane's containment AND, since slice E, its exposure ceiling. The test
// is written against the hand-built map because a fixture from WorstLanesByDomain
// could never exercise it.
func TestForRefusesTheEmptyKeyEvenWhenAMapCarriesOne(t *testing.T) {
	d := DomainLanes{"": LaneQuarantine, "acme.test": LaneHealthy}

	for _, email := range []string{
		"someone@gmail.com", // consumer provider: carved out of the fold
		"broken",            // no @ at all
		"@nohost.test",      // hmm: an address with no local part still has a host
		"nolocal@",          // no host
	} {
		if got := d.For(email); got == LaneQuarantine {
			t.Errorf("For(%q) = %q, read from the empty key — the carve-out must not "+
				"inherit a verdict it never contributed to", email, got)
		}
	}
	// And the real key still resolves, so the guard is not simply refusing everything.
	if got := d.For("a@acme.test"); got != LaneHealthy {
		t.Errorf("For on a real domain = %q, want %q", got, LaneHealthy)
	}
}
