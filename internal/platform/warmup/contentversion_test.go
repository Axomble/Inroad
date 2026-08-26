package warmup

import (
	"context"
	"regexp"
	"strconv"
	"testing"

	"github.com/inroad/inroad/internal/platform/spintax"
)

// contentVersionShape restates the shape migration 000070's CHECK enforces on the
// persisted column. It lives here, not beside the derivation, because the constraint
// is the source of truth and this is an EXPECTATION about it — the production code has
// no need to re-validate its own output.
//
// The scheme segment is open and the digest is FIXED-WIDTH, which is the load-bearing
// half: an unstable value — a uuid, a timestamp, an expanded body, a counter — cannot
// satisfy it. That matters because the read side GROUPs BY this column, so a bad value
// does not fail loudly; it becomes a version of its own and quietly makes every rate in
// the report unreadable. TestPostgresRefusesAContentVersionThatCouldNotBeStable proves
// the real constraint agrees with this pattern.
var contentVersionShape = regexp.MustCompile(`^[a-z0-9]+:[0-9a-f]{16}$`)

// The identifier is a GROUPING KEY, so every test here is really a test of one
// property: two sends of the same library content must land in the same bucket, and
// two sends of different content must not. Everything that could break that —
// process-local state, the thread's position in the library, the spintax draw — gets
// its own case below.

func TestContentVersionIsStableAcrossRepeatedCalls(t *testing.T) {
	first := ContentVersion("mailbox-a:mailbox-b:2026-08-21:0", 0)
	if first == "" {
		t.Fatal("ContentVersion returned empty for a key that resolves to real library content")
	}
	for i := 0; i < 100; i++ {
		if got := ContentVersion("mailbox-a:mailbox-b:2026-08-21:0", 0); got != first {
			t.Fatalf("call %d returned %q, want %q — an identifier that varies within one process "+
				"cannot group anything", i, got, first)
		}
	}
}

// The whole point of fingerprinting the TEMPLATE rather than the expanded body: two
// different content keys that select the same library thread describe the same
// content and must share a version. If this split, every spintax draw would become
// its own "version" with a sample of one.
func TestDifferentContentKeysSelectingTheSameThreadShareOneVersion(t *testing.T) {
	byVersion := map[string]int{}
	for i := 0; i < 500; i++ {
		key := "seed-" + strconv.Itoa(i)
		byVersion[ContentVersion(key, 0)]++
	}
	threads := curatedThreads()
	if len(byVersion) != len(threads) {
		t.Fatalf("500 content keys produced %d distinct turn-0 versions, want %d (one per library thread) — "+
			"the identifier is tracking the key or its expansion instead of the template",
			len(byVersion), len(threads))
	}
	for version, n := range byVersion {
		if n < 2 {
			t.Errorf("version %q was produced by only %d of 500 keys; with %d threads every version "+
				"should be hit many times", version, n, len(threads))
		}
	}
}

// Distinctness, exhaustively over the real corpus: every (thread, turn) pair the
// library can send must have its own identifier. A collision silently merges two
// templates' placement into one rate.
func TestEveryLibraryTurnHasItsOwnVersion(t *testing.T) {
	seen := map[string]string{}
	for i, tmpl := range curatedThreads() {
		for turn := range tmpl.Turns {
			v := contentVersionOf(tmpl, turn)
			where := "thread " + strconv.Itoa(i) + " turn " + strconv.Itoa(turn)
			if prev, dup := seen[v]; dup {
				t.Errorf("%s and %s both hash to %q — two different bodies would be reported as one version",
					prev, where, v)
			}
			seen[v] = where
		}
	}
	if len(seen) < len(curatedThreads()) {
		t.Fatalf("only %d versions over the whole corpus; the loop is not covering it", len(seen))
	}
}

// A turn the template does not have is not content, so it has no version. The send
// path never asks for one (warmup.Reply gates it), but a caller that does must get
// "nothing to attribute" rather than a plausible-looking identifier for a body that
// was never sent.
func TestContentVersionIsEmptyForATurnTheThreadDoesNotHave(t *testing.T) {
	for _, turn := range []int{-1, MaxContentTurns(), MaxContentTurns() + 1, 1 << 20} {
		if got := ContentVersion("any-key", turn); got != "" {
			t.Errorf("ContentVersion(_, %d) = %q, want \"\" — no such turn exists in any library thread",
				turn, got)
		}
	}
}

// Editing one turn must re-version THAT turn and leave its siblings alone, so a
// content fix does not reset the history of the bodies it did not touch. This is
// also the assertion that the fingerprint covers the turn's own text: if it hashed
// the whole thread, all three would move together.
func TestEditingOneTurnReversionsOnlyThatTurn(t *testing.T) {
	before := Thread{Subject: "Invoice reference", Turns: []string{"opener", "reply", "close"}}
	after := Thread{Subject: "Invoice reference", Turns: []string{"opener", "reply, reworded", "close"}}

	if contentVersionOf(before, 1) == contentVersionOf(after, 1) {
		t.Error("rewriting turn 1 did not change turn 1's version — an edited body would keep " +
			"accumulating the old body's placement record")
	}
	for _, turn := range []int{0, 2} {
		if contentVersionOf(before, turn) != contentVersionOf(after, turn) {
			t.Errorf("rewriting turn 1 also re-versioned turn %d, discarding history for a body "+
				"that did not change", turn)
		}
	}
}

// Two turns can legitimately carry the same words — a one-line acknowledgement is the
// obvious case — and they are still two different sends at two different points in a
// conversation. Without the turn index in the digest they would collide, and turn 2's
// placement would be filed under turn 0.
func TestTwoTurnsWithIdenticalWordingStillHaveDifferentVersions(t *testing.T) {
	echo := Thread{Subject: "Cover while I'm out", Turns: []string{"Noted, thanks.", "Noted, thanks."}}
	if contentVersionOf(echo, 0) == contentVersionOf(echo, 1) {
		t.Error("two turns with identical text share a version — a reply's placement would be " +
			"attributed to the opener, and the two do not land the same way")
	}
}

// The subject is sent with every turn, so it is part of what was measured.
func TestChangingTheSubjectChangesEveryTurnsVersion(t *testing.T) {
	before := Thread{Subject: "Invoice reference", Turns: []string{"opener", "reply"}}
	after := Thread{Subject: "Quick one on the invoice", Turns: []string{"opener", "reply"}}

	for turn := range before.Turns {
		if contentVersionOf(before, turn) == contentVersionOf(after, turn) {
			t.Errorf("turn %d kept its version across a subject rewrite, but the subject is part of "+
				"the mail that was measured", turn)
		}
	}
}

// A version must survive the library being reordered or extended. Threads get added
// (the corpus is meant to grow) and an index-derived identifier would silently
// re-label every existing thread's history on the next insert — one template's rate
// would inherit another's samples.
func TestAddingOrReorderingThreadsDoesNotChangeAnExistingThreadsVersion(t *testing.T) {
	target := Thread{Subject: "Meeting room for Thursday", Turns: []string{"ask", "booked"}}
	other := Thread{Subject: "Lunch moved to 1", Turns: []string{"heads up", "fine by me"}}

	// The same thread, sitting at three different positions in three different libraries.
	first := []Thread{target, other}
	last := []Thread{other, target}
	grown := []Thread{other, {Subject: "New", Turns: []string{"x"}}, target}

	want := contentVersionOf(target, 0)
	for name, lib := range map[string][]Thread{"first": first, "last": last, "grown": grown} {
		idx := -1
		for i, th := range lib {
			if th.Subject == target.Subject {
				idx = i
			}
		}
		if idx < 0 {
			t.Fatalf("%s: target thread missing from the fixture", name)
		}
		if got := contentVersionOf(lib[idx], 0); got != want {
			t.Errorf("%s library (target at index %d) gives %q, want %q — the identifier depends on "+
				"POSITION, so adding a thread re-labels the whole corpus's history", name, idx, got, want)
		}
	}
}

// THE cross-restart guard. `go test` is a fresh process every run, so a golden
// recorded in source is the only assertion that can catch an identifier derived from
// something process-local — an address, a map iteration order, a run-scoped salt, the
// clock. Any of those would pass every other test in this file and fail here on the
// second run.
//
// These constants are not a specification of the hash; they are a ratchet. The first
// implementation produced them, and the contract is that they never change again,
// because 7-day history filed under an old value is unreadable after a rename. A
// deliberate change of derivation is a NEW scheme prefix, not an edit to these.
func TestContentVersionMatchesItsRecordedGoldens(t *testing.T) {
	for _, tc := range []struct {
		contentKey string
		turn       int
		want       string
	}{
		{"a@acme.test:b@acme.test:2026-08-21:0", 0, "sl1:43d0dfc37a201f15"},
		{"a@acme.test:b@acme.test:2026-08-21:0", 1, "sl1:092b5835ff6bdcc8"},
		{"", 0, "sl1:9a51289b43c991e0"},
	} {
		if got := ContentVersion(tc.contentKey, tc.turn); got != tc.want {
			t.Errorf("ContentVersion(%q, %d) = %q, want %q — either the derivation changed (every "+
				"existing row's history is now filed under a name nothing computes) or it is not "+
				"stable across processes", tc.contentKey, tc.turn, got, tc.want)
		}
	}
}

// Persistence puts a shape CHECK on this column precisely because an unrecognised
// value does not fail loudly — it becomes a version of its own. Everything the
// library can produce has to satisfy it.
func TestEveryVersionTheLibraryProducesMatchesThePersistedShape(t *testing.T) {
	for i, tmpl := range curatedThreads() {
		for turn := range tmpl.Turns {
			v := contentVersionOf(tmpl, turn)
			if !contentVersionShape.MatchString(v) {
				t.Errorf("thread %d turn %d produced %q, which the 000070 CHECK would reject — the "+
					"send would abort instead of recording", i, turn, v)
			}
		}
	}
}

// The DRIFT GUARD, and the reason it exists: selecting the template is stated twice —
// once in StaticLibrary.Thread, once here — because contentversion.go must not
// reach into the generator to ask "which template did you use". If the two ever
// disagree, versions are attributed to content that was never sent, and nothing else
// in this package would notice.
//
// Re-expanding the template this file selected and comparing it to what the library
// actually returns pins the selection exactly: spintax is seed-deterministic, so
// equality here can only hold when both picked the same template.
func TestTheTemplateFingerprintedIsTheTemplateTheLibrarySends(t *testing.T) {
	lib := NewStaticLibrary()
	for i := 0; i < 200; i++ {
		seed := "drift-" + strconv.Itoa(i)
		sent, err := lib.Thread(context.Background(), seed)
		if err != nil {
			t.Fatalf("library thread for %q: %v", seed, err)
		}
		tmpl, ok := contentTemplateFor(seed)
		if !ok {
			t.Fatalf("no template resolved for %q", seed)
		}
		if got := spintax.Expand(tmpl.Subject, spintax.Seed(seed, "subject")); got != sent.Subject {
			t.Fatalf("seed %q: fingerprinted template expands to subject %q, but the library sent %q — "+
				"the two selection rules have drifted", seed, got, sent.Subject)
		}
		if len(tmpl.Turns) != len(sent.Turns) {
			t.Fatalf("seed %q: fingerprinted template has %d turns, the library sent %d",
				seed, len(tmpl.Turns), len(sent.Turns))
		}
		for turn := range tmpl.Turns {
			want := sent.Turns[turn]
			got := spintax.Expand(tmpl.Turns[turn], spintax.Seed(seed, "turn", strconv.Itoa(turn)))
			if got != want {
				t.Fatalf("seed %q turn %d: fingerprinted template expands to %q, the library sent %q",
					seed, turn, got, want)
			}
		}
	}
}
