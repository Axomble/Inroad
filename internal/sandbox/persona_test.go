package sandbox

import (
	"strings"
	"testing"
)

func TestBuildPersonasEmpty(t *testing.T) {
	for _, n := range []int{0, -1} {
		if got := BuildPersonas(n); got != nil {
			t.Errorf("BuildPersonas(%d) = %v, want nil", n, got)
		}
	}
}

// The addresses land in contacts.email, which is uniquely indexed per
// workspace: a duplicate would make the seeder silently upsert one persona
// onto another and produce fewer contacts than it reported.
func TestPersonaEmailsAreUnique(t *testing.T) {
	const n = 500 // well past one full cycle of every roster table
	seen := make(map[string]int, n)
	for i, p := range BuildPersonas(n) {
		if first, dup := seen[p.Email]; dup {
			t.Fatalf("email %q generated at both index %d and %d", p.Email, first, i)
		}
		seen[p.Email] = i
	}
}

// The harness exists to produce demoable state; an address like test1@test.com
// defeats that. Assert the shape a human would recognise as a work address.
func TestPersonasLookLikeRealPeople(t *testing.T) {
	for i, p := range BuildPersonas(120) {
		if p.FirstName == "" || p.LastName == "" || p.Company == "" || p.Title == "" {
			t.Fatalf("persona %d has an empty field: %+v", i, p)
		}
		local, domain, ok := strings.Cut(p.Email, "@")
		if !ok {
			t.Fatalf("persona %d email %q has no @", i, p.Email)
		}
		if !strings.Contains(local, ".") {
			t.Errorf("persona %d local part %q is not first.last shaped", i, local)
		}
		if domain != p.Domain {
			t.Errorf("persona %d email domain = %q, want the company domain %q", i, domain, p.Domain)
		}
		if strings.Contains(domain, "example.com") || strings.HasPrefix(local, "test") {
			t.Errorf("persona %d looks like a placeholder: %q", i, p.Email)
		}
		if !strings.HasPrefix(local, strings.ToLower(p.FirstName)) {
			t.Errorf("persona %d email %q does not match name %q", i, p.Email, p.FullName())
		}
	}
}

// A demo run and a re-run of an integration test must produce identical state,
// or a screenshot goes stale and a failure stops reproducing.
func TestPersonasAreDeterministic(t *testing.T) {
	a, b := BuildPersonas(60), BuildPersonas(60)
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("persona %d differs between runs:\n a=%+v\n b=%+v", i, a[i], b[i])
		}
	}
}

// The seeded funnel has to be believable: rates far off target make the
// reporting screens useless as a demo and hide real regressions in them.
func TestEngagementRatesAreNearTarget(t *testing.T) {
	const n = 4000
	var opens, clicks, replies, bounces int
	for _, p := range BuildPersonas(n) {
		b := p.Behavior
		switch {
		case b.Bounces:
			bounces++
		case b.Opens:
			opens++
		}
		if b.Clicks {
			clicks++
		}
		if b.Replies {
			replies++
		}
	}

	// Tolerances are wide because the draws are hashes, not a shuffle, and the
	// nested gates mean click/reply are bounded by the open rate. The test is
	// here to catch an order-of-magnitude mistake, not to pin the hash.
	// The rates are stated population-wide and the draws are rescaled by the
	// open rate to realise them (see scaleByOpenRate), so each target here is
	// simply the stated constant.
	cases := []struct {
		name           string
		got            int
		wantPercent    float64
		tolerancePoint float64
	}{
		{"open", opens, openRatePercent, 6},
		{"click", clicks, clickRatePercent, 4},
		{"reply", replies, replyRatePercent, 4},
		{"bounce", bounces, bounceRatePercent, 2},
	}
	for _, c := range cases {
		gotPercent := float64(c.got) / float64(n) * 100
		if diff := gotPercent - c.wantPercent; diff > c.tolerancePoint || diff < -c.tolerancePoint {
			t.Errorf("%s rate = %.1f%%, want %.1f%% +/- %.1f", c.name, gotPercent, c.wantPercent, c.tolerancePoint)
		}
	}
}

// Impossible states would show up as impossible ratios on the reporting
// screens (a click with no open, engagement on a bounced address).
func TestBehaviorCausalityHolds(t *testing.T) {
	for i, p := range BuildPersonas(3000) {
		b := p.Behavior
		if b.Bounces && (b.Opens || b.Clicks || b.Replies) {
			t.Fatalf("persona %d bounced but also engaged: %+v", i, b)
		}
		if b.Clicks && !b.Opens {
			t.Fatalf("persona %d clicked without opening: %+v", i, b)
		}
		if b.Replies && !b.Opens {
			t.Fatalf("persona %d replied without opening: %+v", i, b)
		}
	}
}

// Threads join reply_labels on (workspace_id, key); a key no builtin label
// claims would render the thread with no label at all.
func TestReplyFlavorsMapToSeededLabelKeys(t *testing.T) {
	// The builtins every workspace gets from migration 000047's trigger.
	builtin := map[string]bool{
		"positive": true, "negative": true, "neutral": true, "unsubscribe": true,
		"out_of_office": true, "auto_reply": true, "unknown": true,
	}
	for _, f := range []ReplyFlavor{ReplyPositive, ReplyQuestion, ReplyNegative, ReplyOutOfOffice, ReplyUnsubscribe} {
		if key := f.LabelKey(); !builtin[key] {
			t.Errorf("flavor %d maps to %q, which no builtin reply label claims", f, key)
		}
	}
	if got := ReplyFlavor(99).LabelKey(); got != "unknown" {
		t.Errorf("unrecognised flavor maps to %q, want the seeded %q fallback", got, "unknown")
	}
}

// An inbox showing one reply class repeated is not a useful triage demo.
func TestReplyFlavorsAreMixed(t *testing.T) {
	seen := map[ReplyFlavor]int{}
	for _, p := range BuildPersonas(4000) {
		if p.Behavior.Replies {
			seen[p.Behavior.Reply]++
		}
	}
	for _, f := range []ReplyFlavor{ReplyPositive, ReplyQuestion, ReplyNegative, ReplyOutOfOffice, ReplyUnsubscribe} {
		if seen[f] == 0 {
			t.Errorf("no persona ever replies with flavor %d", f)
		}
	}
}
