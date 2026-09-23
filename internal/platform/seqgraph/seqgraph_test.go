package seqgraph

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
)

// ids returns n deterministic, distinct step ids so failures print stably.
func ids(n int) []uuid.UUID {
	out := make([]uuid.UUID, n)
	for i := range out {
		out[i] = uuid.NewSHA1(uuid.NameSpaceOID, []byte{byte(i + 1)})
	}
	return out
}

// linear builds steps 1..n with the given delays (all 0 when omitted).
func linear(id []uuid.UUID) []Step {
	steps := make([]Step, len(id))
	for i := range id {
		steps[i] = Step{ID: id[i], Order: int32(i + 1), DelaySeconds: int32(i * 60)}
	}
	return steps
}

func TestParseCondition(t *testing.T) {
	for _, c := range []Condition{Always, Opened, Clicked, Replied, NotOpened, NotReplied} {
		got, ok := ParseCondition(string(c))
		if !ok || got != c {
			t.Errorf("ParseCondition(%q) = %q, %v", c, got, ok)
		}
	}
	for _, bad := range []string{"", "OPENED", "bounced", "not-opened"} {
		if _, ok := ParseCondition(bad); ok {
			t.Errorf("ParseCondition(%q) accepted an unknown condition", bad)
		}
	}
}

func TestValidateShape(t *testing.T) {
	id := ids(3)
	cases := []struct {
		name string
		b    Branch
		code string // "" = valid
	}{
		{"always to a step", Branch{StepID: id[0], Condition: Always, Yes: id[1]}, ""},
		{"always to the end", Branch{StepID: id[0], Condition: Always}, ""},
		{"opened both exits", Branch{StepID: id[0], Condition: Opened, WithinDays: 3, Yes: id[1], No: id[2]}, ""},
		{"replied with label", Branch{StepID: id[0], Condition: Replied, WithinDays: 1, ReplyLabelKey: "positive"}, ""},
		{"not_replied with label", Branch{StepID: id[0], Condition: NotReplied, WithinDays: 90, ReplyLabelKey: "out_of_office"}, ""},
		{"unknown condition", Branch{StepID: id[0], Condition: "bounced", WithinDays: 3}, CodeInvalidCondition},
		{"condition without window", Branch{StepID: id[0], Condition: Opened}, CodeInvalidWithinDays},
		{"window above max", Branch{StepID: id[0], Condition: Opened, WithinDays: MaxWithinDays + 1}, CodeInvalidWithinDays},
		{"negative window", Branch{StepID: id[0], Condition: Clicked, WithinDays: -1}, CodeInvalidWithinDays},
		{"always with window", Branch{StepID: id[0], Condition: Always, WithinDays: 3}, CodeInvalidWithinDays},
		{"always with a no exit", Branch{StepID: id[0], Condition: Always, No: id[1]}, CodeNoExitNotAllowed},
		{"label on an open condition", Branch{StepID: id[0], Condition: Opened, WithinDays: 3, ReplyLabelKey: "positive"}, CodeLabelNotAllowed},
		{"malformed label key", Branch{StepID: id[0], Condition: Replied, WithinDays: 3, ReplyLabelKey: "Positive!"}, CodeLabelNotAllowed},
		{"yes to itself", Branch{StepID: id[0], Condition: Opened, WithinDays: 3, Yes: id[0]}, CodeCycle},
		{"no to itself", Branch{StepID: id[0], Condition: Opened, WithinDays: 3, No: id[0]}, CodeCycle},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.b.ValidateShape()
			if tc.code == "" {
				if err != nil {
					t.Fatalf("want valid, got %v", err)
				}
				return
			}
			if got := CodeOf(err); got != tc.code {
				t.Fatalf("code = %q (err %v), want %q", got, err, tc.code)
			}
		})
	}
}

func TestValidateLinearIsAcyclic(t *testing.T) {
	g := New(linear(ids(4)), nil)
	if err := g.Validate(); err != nil {
		t.Fatalf("a linear sequence must validate: %v", err)
	}
	if g.HasBranches() {
		t.Fatal("no branches were given")
	}
}

func TestValidateEmptyGraph(t *testing.T) {
	if err := New(nil, nil).Validate(); err != nil {
		t.Fatalf("an empty campaign must validate: %v", err)
	}
}

func TestValidateRejectsExplicitCycle(t *testing.T) {
	id := ids(3)
	// 1 -always-> 2 -always-> 3 -always-> 1
	g := New(linear(id), []Branch{
		{StepID: id[0], Condition: Always, Yes: id[1]},
		{StepID: id[1], Condition: Always, Yes: id[2]},
		{StepID: id[2], Condition: Always, Yes: id[0]},
	})
	var cyc *CycleError
	if err := g.Validate(); !errors.As(err, &cyc) {
		t.Fatalf("want CycleError, got %v", err)
	}
	if CodeOf(cyc) != CodeCycle {
		t.Fatalf("cycle code = %q", CodeOf(cyc))
	}
	for _, want := range id {
		if !slices.Contains(cyc.StepIDs, want) {
			t.Fatalf("cycle %v is missing step %v", cyc.StepIDs, want)
		}
	}
}

// The cycle a naive check misses: only ONE explicit edge, closed by the implicit
// "next by step_order" fall-through of steps that have no branch.
func TestValidateRejectsCycleThroughFallThrough(t *testing.T) {
	id := ids(3)
	g := New(linear(id), []Branch{
		// Step 3 jumps back to step 1; 1 -> 2 -> 3 is implicit.
		{StepID: id[2], Condition: Always, Yes: id[0]},
	})
	var cyc *CycleError
	if err := g.Validate(); !errors.As(err, &cyc) {
		t.Fatalf("want CycleError for a cycle closed by fall-through, got %v", err)
	}
	if len(cyc.StepIDs) != 3 {
		t.Fatalf("cycle = %v, want all three steps", cyc.StepIDs)
	}
}

// A conditional's NO exit is an edge like any other.
func TestValidateRejectsCycleThroughNoExit(t *testing.T) {
	id := ids(3)
	g := New(linear(id), []Branch{
		{StepID: id[1], Condition: NotOpened, WithinDays: 2, Yes: id[2], No: id[0]},
	})
	if err := g.Validate(); CodeOf(err) != CodeCycle {
		t.Fatalf("want cycle via the no exit, got %v", err)
	}
}

// A backward edge is NOT a cycle when the target cannot reach the source: here
// step 3 jumps to step 2, and step 2 ends explicitly.
func TestValidateAllowsBackwardEdgeWithoutCycle(t *testing.T) {
	id := ids(3)
	g := New(linear(id), []Branch{
		{StepID: id[0], Condition: Opened, WithinDays: 3, Yes: id[2], No: id[1]},
		{StepID: id[1], Condition: Always},             // 2 ends
		{StepID: id[2], Condition: Always, Yes: id[1]}, // 3 -> 2 (backward, acyclic)
	})
	if err := g.Validate(); err != nil {
		t.Fatalf("1 -> {3 -> 2, 2} is acyclic: %v", err)
	}
}

func TestValidateRejectsTargetOutsideCampaign(t *testing.T) {
	id := ids(3)
	foreign := uuid.New()
	g := New(linear(id[:2]), []Branch{
		{StepID: id[0], Condition: Replied, WithinDays: 3, Yes: foreign},
	})
	var te *TargetError
	if err := g.Validate(); !errors.As(err, &te) {
		t.Fatalf("want TargetError, got %v", err)
	}
	if te.Target != foreign || te.StepID != id[0] || CodeOf(te) != CodeUnknownTarget {
		t.Fatalf("TargetError = %+v", te)
	}
}

func TestValidateRejectsBranchOnForeignStep(t *testing.T) {
	id := ids(2)
	g := New(linear(id[:1]), []Branch{{StepID: id[1], Condition: Always}})
	if err := g.Validate(); CodeOf(err) != CodeUnknownStep {
		t.Fatalf("want %q, got %v", CodeUnknownStep, err)
	}
}

func TestValidateReportsShapeErrors(t *testing.T) {
	id := ids(2)
	g := New(linear(id), []Branch{{StepID: id[0], Condition: Opened}})
	if err := g.Validate(); CodeOf(err) != CodeInvalidWithinDays {
		t.Fatalf("Validate must include the per-branch shape check, got %v", err)
	}
}

func TestDefaultNextToleratesGaps(t *testing.T) {
	id := ids(3)
	g := New([]Step{
		{ID: id[2], Order: 7},
		{ID: id[0], Order: 1},
		{ID: id[1], Order: 3},
	}, nil)
	if n, ok := g.DefaultNext(id[0]); !ok || n.ID != id[1] {
		t.Fatalf("next after order 1 = %+v %v, want order 3", n, ok)
	}
	if n, ok := g.DefaultNext(id[1]); !ok || n.ID != id[2] {
		t.Fatalf("next after order 3 = %+v %v, want order 7", n, ok)
	}
	if _, ok := g.DefaultNext(id[2]); ok {
		t.Fatal("the last step has no default next")
	}
	if e, ok := g.Entry(); !ok || e.ID != id[0] {
		t.Fatalf("entry = %+v %v", e, ok)
	}
	if s, ok := g.StepByOrder(3); !ok || s.ID != id[1] {
		t.Fatalf("StepByOrder(3) = %+v %v", s, ok)
	}
	if _, ok := g.StepByOrder(2); ok {
		t.Fatal("order 2 is a gap")
	}
}

func TestAfter(t *testing.T) {
	id := ids(4)
	g := New(linear(id), []Branch{
		{StepID: id[0], Condition: Opened, WithinDays: 2, Yes: id[2], No: id[1]},
		{StepID: id[1], Condition: Always, Yes: id[3]},
		{StepID: id[2], Condition: Always},
	})

	if p := g.After(id[0]); p.Kind != PlanAwait || p.Branch.Condition != Opened {
		t.Fatalf("step 1 has a condition: %+v", p)
	}
	if p := g.After(id[1]); p.Kind != PlanNext || p.Next.ID != id[3] {
		t.Fatalf("step 2 always -> 4: %+v", p)
	}
	if p := g.After(id[2]); p.Kind != PlanEnd {
		t.Fatalf("step 3 always -> end: %+v", p)
	}
	if p := g.After(id[3]); p.Kind != PlanEnd {
		t.Fatalf("step 4 is last with no branch: %+v", p)
	}

	// No branch at all: fall through by order — the linear rule.
	lin := New(linear(id), nil)
	if p := lin.After(id[1]); p.Kind != PlanNext || p.Next.ID != id[2] {
		t.Fatalf("linear step 2 -> 3: %+v", p)
	}
	// An unknown step (deleted while an enrollment sat on it) has no plan to
	// follow; the caller decides.
	if p := lin.After(uuid.New()); p.Kind != PlanEnd {
		t.Fatalf("unknown step: %+v", p)
	}
}

// An exit whose target no longer exists in the graph ends the path rather than
// routing into nothing. The database nulls such exits (ON DELETE SET NULL); this
// is the in-memory half of the same rule.
func TestAfterMissingTargetEnds(t *testing.T) {
	id := ids(2)
	g := New(linear(id[:1]), []Branch{{StepID: id[0], Condition: Always, Yes: id[1]}})
	if p := g.After(id[0]); p.Kind != PlanEnd {
		t.Fatalf("always -> missing step must end: %+v", p)
	}
}

func TestEvaluate(t *testing.T) {
	start := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	window := 3 * 24 * time.Hour
	deadline := start.Add(window)
	early := start.Add(5 * time.Hour)
	late := deadline.Add(time.Hour)
	var none time.Time

	cases := []struct {
		name    string
		cond    Condition
		first   time.Time
		now     time.Time
		decided bool
		yes     bool
		at      time.Time
	}{
		// Positive conditions: the event decides YES the moment it happens.
		{"opened early", Opened, early, early.Add(time.Minute), true, true, early},
		{"clicked early", Clicked, early, deadline.Add(-time.Minute), true, true, early},
		{"replied early", Replied, early, early, true, true, early},
		// ...and the window closing without it decides NO, at the deadline.
		{"opened window closed", Opened, none, deadline, true, false, deadline},
		{"replied window long closed", Replied, none, late, true, false, deadline},
		// Still open, nothing seen: wait.
		{"opened pending", Opened, none, early, false, false, deadline},
		// Negated conditions: the event decides NO immediately...
		{"not_opened but opened", NotOpened, early, early, true, false, early},
		{"not_replied but replied", NotReplied, early, early.Add(time.Hour), true, false, early},
		// ...and silence until the deadline decides YES.
		{"not_opened window closed", NotOpened, none, deadline, true, true, deadline},
		{"not_replied pending", NotReplied, none, early, false, false, deadline},
		// An event after the window does not count, whatever the clock says now.
		{"opened after window", Opened, late, late, true, false, deadline},
		{"not_opened open after window", NotOpened, late, late.Add(time.Hour), true, true, deadline},
		// Exactly on the deadline is inside the window.
		{"opened on the deadline", Opened, deadline, deadline, true, true, deadline},
		// An event stamped before the window start (clock skew between the send
		// and the cursor stamp) decides at the start, never before it.
		{"opened before start", Opened, start.Add(-time.Second), start, true, true, start},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := Evaluate(tc.cond, start, window, tc.first, tc.now)
			if v.Decided != tc.decided || v.Yes != tc.yes || !v.At.Equal(tc.at) {
				t.Fatalf("Evaluate = %+v, want decided=%v yes=%v at=%v", v, tc.decided, tc.yes, tc.at)
			}
		})
	}
}

// 'always' is not evaluated against evidence; it is routed by After. Evaluate
// must still be total, and decides YES immediately so a caller that asks cannot
// wedge an enrollment.
func TestEvaluateAlwaysIsImmediateYes(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	v := Evaluate(Always, start, 0, time.Time{}, start)
	if !v.Decided || !v.Yes || !v.At.Equal(start) {
		t.Fatalf("Evaluate(always) = %+v", v)
	}
}

func TestBranchTarget(t *testing.T) {
	id := ids(3)
	b := Branch{StepID: id[0], Condition: Opened, WithinDays: 1, Yes: id[1], No: id[2]}
	if got := b.Target(Verdict{Decided: true, Yes: true}); got != id[1] {
		t.Fatalf("yes target = %v", got)
	}
	if got := b.Target(Verdict{Decided: true, Yes: false}); got != id[2] {
		t.Fatalf("no target = %v", got)
	}
	if got := (Branch{Condition: Opened, WithinDays: 1}).Target(Verdict{Decided: true, Yes: true}); got != uuid.Nil {
		t.Fatalf("an unset exit is the end, got %v", got)
	}
	if b.Window() != 24*time.Hour {
		t.Fatalf("window = %v", b.Window())
	}
}

func TestSignal(t *testing.T) {
	cases := map[Condition]Signal{
		Always: SignalNone, Opened: SignalOpen, NotOpened: SignalOpen,
		Clicked: SignalClick, Replied: SignalReply, NotReplied: SignalReply,
	}
	for c, want := range cases {
		if got := c.Signal(); got != want {
			t.Errorf("%q.Signal() = %v, want %v", c, got, want)
		}
	}
}
