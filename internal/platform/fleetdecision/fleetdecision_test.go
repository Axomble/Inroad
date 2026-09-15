package fleetdecision

import (
	"regexp"
	"strings"
	"testing"
)

// scoreShaped matches anything that reads as a numeric score. A FORCED decision
// must contain nothing that matches it: the whole failure this package exists to
// prevent is a log line showing a comparison against a 0.00 that was never
// computed.
var scoreShaped = regexp.MustCompile(`\d+\.\d+`)

func TestChoseNamesBothCandidatesAndBothScores(t *testing.T) {
	r := Chose(
		Candidate{WorkerID: "w-alpha", Score: 0.82},
		Candidate{WorkerID: "w-beta", Score: 0.61},
		4,
	)
	if !r.Valid() {
		t.Fatal("Chose produced an invalid Reason")
	}
	got := r.String()
	for _, want := range []string{"w-alpha", "0.82", "w-beta", "0.61", "4"} {
		if !strings.Contains(got, want) {
			t.Errorf("Chose reason = %q, want it to mention %q", got, want)
		}
	}
}

func TestChoseUncontestedNamesNoRunnerUp(t *testing.T) {
	r := ChoseUncontested(Candidate{WorkerID: "w-alpha", Score: 0.82}, 1)
	got := r.String()
	if !strings.Contains(got, "w-alpha") || !strings.Contains(got, "0.82") {
		t.Errorf("ChoseUncontested reason = %q, want it to name the winner and its score", got)
	}
	if strings.Contains(got, " over ") {
		t.Errorf("ChoseUncontested reason = %q, want no comparison — there was no runner-up", got)
	}
}

// The rule, stated as a test: a decision that was FORCED rather than chosen must
// not print a score at all. A worker that was picked because it was the only
// live one, or because the incumbent was unhealthy, was not scored against
// anything — rendering it "0.82 over 0.00" invents a comparison, and a log that
// lies is worse than no log.
func TestForcedReasonPrintsNoScore(t *testing.T) {
	for _, cause := range []string{
		"incumbent worker stopped heartbeating",
		"the mailbox's risk band has no live worker and no idle worker to adopt one",
		"single-worker fleet: no placement choice exists",
	} {
		r := Forced(cause)
		if !r.Valid() {
			t.Fatalf("Forced(%q) produced an invalid Reason", cause)
		}
		got := r.String()
		if scoreShaped.MatchString(got) {
			t.Errorf("Forced(%q) = %q, which contains a score-shaped number", cause, got)
		}
		if strings.Contains(got, " over ") {
			t.Errorf("Forced(%q) = %q, which reads as a comparison", cause, got)
		}
		if !strings.Contains(got, cause) {
			t.Errorf("Forced(%q) = %q, want the operator-actionable cause to survive", cause, got)
		}
	}
}

// The rule has to hold against a CALLER's mistake too, not just against a
// careful one. A caller with no real runner-up that reaches for Chose anyway
// (passing a zero Candidate) must not be able to publish "w-alpha 0.82 over
// "" 0.00" — the comparison degrades to the uncontested form instead.
func TestChoseWithoutARealRunnerUpDegradesToUncontested(t *testing.T) {
	got := Chose(Candidate{WorkerID: "w-alpha", Score: 0.82}, Candidate{}, 1).String()
	if strings.Contains(got, " over ") {
		t.Errorf("Chose with a zero runner-up = %q, want no comparison", got)
	}
	if strings.Contains(got, "0.00") {
		t.Errorf("Chose with a zero runner-up = %q, want no invented 0.00 score", got)
	}
	if want := ChoseUncontested(Candidate{WorkerID: "w-alpha", Score: 0.82}, 1).String(); got != want {
		t.Errorf("Chose with a zero runner-up = %q, want it identical to ChoseUncontested's %q", got, want)
	}
}

// The zero Reason must be unusable, so a call site cannot bypass the
// constructors by building an Entry literal with an empty Reason field.
func TestZeroReasonIsInvalid(t *testing.T) {
	var r Reason
	if r.Valid() {
		t.Fatal("the zero Reason reports valid; a call site could then hand-build prose")
	}
	if r.String() != "" {
		t.Errorf("zero Reason prints %q, want empty", r.String())
	}
}

func TestActorFormatting(t *testing.T) {
	cases := []struct {
		name  string
		actor Actor
		want  string
	}{
		{"auto assign", Auto(KindAssign), "auto:assign"},
		{"auto rotate", Auto(KindRotate), "auto:rotate"},
		{"auto quarantine", Auto(KindQuarantine), "auto:quarantine"},
		{"auto refused", Auto(KindRefused), "auto:refused"},
		{"operator", Operator("3f1c9a2e"), "operator:3f1c9a2e"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if string(tc.actor) != tc.want {
				t.Fatalf("actor = %q, want %q", tc.actor, tc.want)
			}
		})
	}
}

// An actor with no id would persist as the bare string "operator:", which names
// nobody and is indistinguishable from a bug.
func TestOperatorWithoutAnIDIsRejected(t *testing.T) {
	if got := Operator(""); got != "" {
		t.Fatalf("Operator(\"\") = %q, want the empty Actor so Validate rejects it", got)
	}
}

func TestEntryValidate(t *testing.T) {
	valid := Entry{
		Kind:        KindAssign,
		WorkerID:    "w-alpha",
		MailboxID:   "0b2b1f3e-0000-0000-0000-000000000001",
		WorkspaceID: "0b2b1f3e-0000-0000-0000-000000000002",
		Reason:      Forced("only live worker"),
		TriggeredBy: Auto(KindAssign),
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("a well-formed entry was rejected: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(Entry) Entry
	}{
		{"unknown kind", func(e Entry) Entry { e.Kind = "vibes"; return e }},
		{"empty reason", func(e Entry) Entry { e.Reason = Reason{}; return e }},
		{"empty actor", func(e Entry) Entry { e.TriggeredBy = ""; return e }},
		// The table's pairing CHECK: a mailbox-scoped decision carries its
		// tenant, a fleet-scoped one carries neither. Catching it here turns a
		// constraint violation into a message that says what was wrong.
		{"mailbox without workspace", func(e Entry) Entry { e.WorkspaceID = ""; return e }},
		{"workspace without mailbox", func(e Entry) Entry { e.MailboxID = ""; return e }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.mutate(valid).Validate(); err == nil {
				t.Fatalf("%s was accepted", tc.name)
			}
		})
	}
}

// A decision about the fleet itself names no mailbox and therefore no tenant.
func TestFleetScopedEntryNeedsNeitherMailboxNorWorkspace(t *testing.T) {
	e := Entry{
		Kind:        KindQuarantine,
		WorkerID:    "w-alpha",
		Reason:      Forced("provider blocked this egress IP"),
		TriggeredBy: Auto(KindQuarantine),
	}
	if err := e.Validate(); err != nil {
		t.Fatalf("a fleet-scoped entry was rejected: %v", err)
	}
}

// Every Kind must be in the persisted vocabulary, or the insert fails its CHECK.
func TestKnownKindsMatchThePersistedVocabulary(t *testing.T) {
	for _, k := range []Kind{KindAssign, KindRotate, KindQuarantine, KindRefused} {
		if !k.Known() {
			t.Errorf("%q is declared but not Known()", k)
		}
	}
	if Kind("release").Known() {
		t.Error("an undeclared kind reports Known(); the CHECK constraint would reject it")
	}
}
