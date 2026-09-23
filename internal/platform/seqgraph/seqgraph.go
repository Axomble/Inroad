// Package seqgraph is the pure routing model of a branched campaign sequence:
// which step an enrollment goes to after the one it just received, whether a
// condition on that step has been decided yet, and whether a proposed graph is
// safe to save.
//
// It does no I/O. The control plane loads steps, branches and the engagement
// evidence, and asks this package what they mean; the sequence-step service asks
// it whether an edit may be committed. Keeping both callers on one definition is
// the point — a cycle rule that the save path and the send path disagreed on
// would let a graph be saved that the send path then loops on.
//
// # The model
//
// Every step is a node. A step's exits are:
//   - its Branch, when it has one: 'always' has one exit (Yes), every real
//     condition has two (Yes, No). An unset exit (uuid.Nil) ends the path.
//   - otherwise the next step by step_order — the linear fall-through every
//     campaign had before branching existed, so a campaign with no branches is
//     exactly the linear sequence.
//
// The fall-through is a real edge for cycle detection: 1 -> 2 -> 3 with a single
// explicit 3 -> 1 is a cycle even though only one edge was drawn.
package seqgraph

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"time"

	"github.com/google/uuid"
)

// Condition is what a branch tests. Values are the stored
// sequence_step_branches.condition strings and the API enum.
type Condition string

const (
	// Always routes unconditionally to Yes (uuid.Nil = end the path). It is how
	// a path ends early or jumps somewhere other than the next step by order.
	Always     Condition = "always"
	Opened     Condition = "opened"
	Clicked    Condition = "clicked"
	Replied    Condition = "replied"
	NotOpened  Condition = "not_opened"
	NotReplied Condition = "not_replied"
)

// ParseCondition maps a stored/API string onto a known condition.
func ParseCondition(s string) (Condition, bool) {
	c := Condition(s)
	switch c {
	case Always, Opened, Clicked, Replied, NotOpened, NotReplied:
		return c, true
	}
	return "", false
}

// Signal is the engagement a condition watches.
type Signal int

const (
	SignalNone Signal = iota
	SignalOpen
	SignalClick
	SignalReply
)

// Signal reports which engagement evidence decides c.
func (c Condition) Signal() Signal {
	switch c {
	case Opened, NotOpened:
		return SignalOpen
	case Clicked:
		return SignalClick
	case Replied, NotReplied:
		return SignalReply
	}
	return SignalNone
}

// negated reports whether the condition is satisfied by the ABSENCE of its
// signal, so that seeing the signal decides it false.
func (c Condition) negated() bool { return c == NotOpened || c == NotReplied }

// Window bounds, in days. A day minimum because every engagement signal here is
// human-paced; 90 because a sequence waiting a quarter for one open is almost
// certainly a mis-set value, and a bounded window is what guarantees every wait
// ends.
const (
	MinWithinDays = 1
	MaxWithinDays = 90
)

// Step is one node: the step's id, its position, and its delay (the minimum gap
// between the decision that routes to it and its send).
type Step struct {
	ID           uuid.UUID
	Order        int32
	DelaySeconds int32
}

// Branch is the router on one step.
type Branch struct {
	StepID     uuid.UUID
	Condition  Condition
	WithinDays int
	// ReplyLabelKey narrows a reply condition to one reply label ("" = any
	// human reply).
	ReplyLabelKey string
	// Yes and No are the exits; uuid.Nil ends the path. 'always' uses Yes only.
	Yes uuid.UUID
	No  uuid.UUID
}

// Window is how long after the source step's send the condition watches.
func (b Branch) Window() time.Duration {
	return time.Duration(b.WithinDays) * 24 * time.Hour
}

// Target is the exit a decided verdict takes (uuid.Nil = end).
func (b Branch) Target(v Verdict) uuid.UUID {
	if v.Yes {
		return b.Yes
	}
	return b.No
}

// exits lists the branch's set exits, Yes first.
func (b Branch) exits() []uuid.UUID {
	var out []uuid.UUID
	for _, id := range []uuid.UUID{b.Yes, b.No} {
		if id != uuid.Nil {
			out = append(out, id)
		}
	}
	return out
}

// Validation codes. Stable strings: the API returns them as `code` so a client
// can react to the failure without parsing the message.
const (
	CodeInvalidCondition  = "invalid_condition"
	CodeInvalidWithinDays = "invalid_within_days"
	CodeLabelNotAllowed   = "invalid_reply_label"
	// CodeLabelStopsSequence is a reply label whose replies stop the enrollment,
	// so a branch naming it could never route anyone.
	CodeLabelStopsSequence = "reply_label_stops_sequence"
	// CodeTrackingRequired is an open/click condition on a campaign or step that
	// can never record an open or click.
	CodeTrackingRequired = "tracking_required"
	CodeNoExitNotAllowed = "no_exit_not_allowed"
	CodeUnknownStep      = "unknown_step"
	CodeUnknownTarget    = "unknown_target"
	CodeCycle            = "cycle"
)

// ShapeError is a branch that is malformed on its own, before any graph is
// considered.
type ShapeError struct {
	Code string
	Msg  string
}

func (e *ShapeError) Error() string { return e.Msg }

// TargetError is an exit pointing at a step that is not in the campaign.
type TargetError struct {
	StepID uuid.UUID
	Target uuid.UUID
}

func (e *TargetError) Error() string {
	return fmt.Sprintf("branch on step %s points at %s, which is not a step of this campaign", e.StepID, e.Target)
}

// UnknownStepError is a branch whose source step is not in the campaign.
type UnknownStepError struct{ StepID uuid.UUID }

func (e *UnknownStepError) Error() string {
	return fmt.Sprintf("step %s is not a step of this campaign", e.StepID)
}

// CycleError is a graph in which some path revisits a step. StepIDs is the loop,
// in path order, starting at the step the loop returns to.
type CycleError struct{ StepIDs []uuid.UUID }

func (e *CycleError) Error() string {
	return fmt.Sprintf("the sequence would loop through %d step(s); every path must end", len(e.StepIDs))
}

// CodeOf returns the validation code carried by err, or "" for an error that is
// not a validation failure.
func CodeOf(err error) string {
	var shape *ShapeError
	var target *TargetError
	var unknown *UnknownStepError
	var cycle *CycleError
	switch {
	case errors.As(err, &shape):
		return shape.Code
	case errors.As(err, &target):
		return CodeUnknownTarget
	case errors.As(err, &unknown):
		return CodeUnknownStep
	case errors.As(err, &cycle):
		return CodeCycle
	}
	return ""
}

// labelKeyPattern mirrors reply_labels.key's CHECK (migration 000047), so a key
// that could never name a label is refused at the boundary rather than by the
// database.
var labelKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// ValidateShape checks the branch on its own: a known condition, a window
// exactly when one is meaningful, a label only on a reply condition, one exit on
// 'always', and no exit back to itself.
func (b Branch) ValidateShape() error {
	if _, ok := ParseCondition(string(b.Condition)); !ok {
		return &ShapeError{Code: CodeInvalidCondition, Msg: fmt.Sprintf("unknown condition %q", b.Condition)}
	}
	if b.Condition == Always {
		if b.WithinDays != 0 {
			return &ShapeError{Code: CodeInvalidWithinDays, Msg: "within_days is not allowed on an 'always' branch"}
		}
		if b.No != uuid.Nil {
			return &ShapeError{Code: CodeNoExitNotAllowed, Msg: "an 'always' branch has only a yes exit"}
		}
	} else if b.WithinDays < MinWithinDays || b.WithinDays > MaxWithinDays {
		return &ShapeError{Code: CodeInvalidWithinDays,
			Msg: fmt.Sprintf("within_days must be between %d and %d", MinWithinDays, MaxWithinDays)}
	}
	if b.ReplyLabelKey != "" {
		if b.Condition.Signal() != SignalReply {
			return &ShapeError{Code: CodeLabelNotAllowed, Msg: "reply_label_key is only allowed on a replied/not_replied branch"}
		}
		if !labelKeyPattern.MatchString(b.ReplyLabelKey) {
			return &ShapeError{Code: CodeLabelNotAllowed, Msg: "reply_label_key is not a valid label key"}
		}
	}
	if b.Yes == b.StepID || b.No == b.StepID {
		return &CycleError{StepIDs: []uuid.UUID{b.StepID}}
	}
	return nil
}

// Graph is one campaign's steps and branches.
type Graph struct {
	ordered  []Step // by Order ascending
	byID     map[uuid.UUID]Step
	branches map[uuid.UUID]Branch
	// branchOrder keeps validation deterministic: the order branches were given.
	branchOrder []uuid.UUID
}

// New builds a graph. Steps may arrive in any order; step_order gaps (left by a
// delete, which does not renumber) are tolerated exactly as the linear send path
// tolerates them.
func New(steps []Step, branches []Branch) Graph {
	g := Graph{
		ordered:  slices.Clone(steps),
		byID:     make(map[uuid.UUID]Step, len(steps)),
		branches: make(map[uuid.UUID]Branch, len(branches)),
	}
	slices.SortFunc(g.ordered, func(a, b Step) int { return int(a.Order) - int(b.Order) })
	for _, s := range g.ordered {
		g.byID[s.ID] = s
	}
	for _, b := range branches {
		if _, dup := g.branches[b.StepID]; !dup {
			g.branchOrder = append(g.branchOrder, b.StepID)
		}
		g.branches[b.StepID] = b
	}
	return g
}

// HasBranches reports whether any step has a router. A graph without one is the
// linear sequence.
func (g Graph) HasBranches() bool { return len(g.branches) > 0 }

// Entry is the first step (lowest step_order), where every enrollment starts.
func (g Graph) Entry() (Step, bool) {
	if len(g.ordered) == 0 {
		return Step{}, false
	}
	return g.ordered[0], true
}

// Step looks a step up by id.
func (g Graph) Step(id uuid.UUID) (Step, bool) {
	s, ok := g.byID[id]
	return s, ok
}

// StepByOrder looks a step up by its step_order — the enrollment cursor's unit.
func (g Graph) StepByOrder(order int32) (Step, bool) {
	i, found := slices.BinarySearchFunc(g.ordered, order, func(s Step, o int32) int { return int(s.Order) - int(o) })
	if !found {
		return Step{}, false
	}
	return g.ordered[i], true
}

// Branch returns the router on a step, if it has one.
func (g Graph) Branch(stepID uuid.UUID) (Branch, bool) {
	b, ok := g.branches[stepID]
	return b, ok
}

// DefaultNext is the linear fall-through: the next step by step_order.
func (g Graph) DefaultNext(stepID uuid.UUID) (Step, bool) {
	s, ok := g.byID[stepID]
	if !ok {
		return Step{}, false
	}
	i, _ := slices.BinarySearchFunc(g.ordered, s.Order+1, func(x Step, o int32) int { return int(x.Order) - int(o) })
	if i >= len(g.ordered) {
		return Step{}, false
	}
	return g.ordered[i], true
}

// exits lists a step's effective successors: its branch's set exits, or the
// linear fall-through when it has no branch.
func (g Graph) exits(stepID uuid.UUID) []uuid.UUID {
	if b, ok := g.branches[stepID]; ok {
		return b.exits()
	}
	if n, ok := g.DefaultNext(stepID); ok {
		return []uuid.UUID{n.ID}
	}
	return nil
}

// Validate checks every branch's shape, that every branch and exit names a step
// of this campaign, and that no path revisits a step. The first problem found is
// returned; the order is deterministic (branches as given, then steps by order).
func (g Graph) Validate() error {
	for _, id := range g.branchOrder {
		b := g.branches[id]
		if err := b.ValidateShape(); err != nil {
			return err
		}
		if _, ok := g.byID[b.StepID]; !ok {
			return &UnknownStepError{StepID: b.StepID}
		}
		for _, t := range b.exits() {
			if _, ok := g.byID[t]; !ok {
				return &TargetError{StepID: b.StepID, Target: t}
			}
		}
	}
	return g.findCycle()
}

// findCycle is a three-colour depth-first search over the effective edges,
// iterative so a long sequence cannot grow the goroutine stack without bound.
func (g Graph) findCycle() error {
	const (
		white = iota
		grey
		black
	)
	colour := make(map[uuid.UUID]int, len(g.ordered))
	type frame struct {
		id    uuid.UUID
		exits []uuid.UUID
		next  int
	}
	for _, root := range g.ordered {
		if colour[root.ID] != white {
			continue
		}
		stack := []frame{{id: root.ID, exits: g.exits(root.ID)}}
		colour[root.ID] = grey
		for len(stack) > 0 {
			top := &stack[len(stack)-1]
			if top.next == len(top.exits) {
				colour[top.id] = black
				stack = stack[:len(stack)-1]
				continue
			}
			child := top.exits[top.next]
			top.next++
			switch colour[child] {
			case grey:
				// The loop is the stack from child's frame to the top.
				var loop []uuid.UUID
				for i := range stack {
					if stack[i].id == child {
						for _, f := range stack[i:] {
							loop = append(loop, f.id)
						}
						break
					}
				}
				return &CycleError{StepIDs: loop}
			case white:
				colour[child] = grey
				stack = append(stack, frame{id: child, exits: g.exits(child)})
			}
		}
	}
	return nil
}

// PlanKind is what happens after a step has been sent.
type PlanKind int

const (
	// PlanEnd: the path ends after this step; the enrollment completes.
	PlanEnd PlanKind = iota
	// PlanNext: Next is sent next, DelaySeconds after this step's send.
	PlanNext
	// PlanAwait: a condition must be decided first; see Branch.
	PlanAwait
)

// Plan is the route out of one step.
type Plan struct {
	Kind   PlanKind
	Next   Step
	Branch Branch
}

// After is the route out of stepID. An exit to a step that is not in the graph
// ends the path — the database already nulls such exits when a step is deleted,
// and routing into nothing is never the right answer. An unknown stepID also
// yields PlanEnd; callers that can do better (the send path falls back to the
// linear rule for a cursor on a deleted step) check Step first.
func (g Graph) After(stepID uuid.UUID) Plan {
	if _, ok := g.byID[stepID]; !ok {
		return Plan{Kind: PlanEnd}
	}
	b, ok := g.branches[stepID]
	if !ok {
		if n, ok := g.DefaultNext(stepID); ok {
			return Plan{Kind: PlanNext, Next: n}
		}
		return Plan{Kind: PlanEnd}
	}
	if b.Condition != Always {
		return Plan{Kind: PlanAwait, Branch: b}
	}
	if n, ok := g.byID[b.Yes]; ok {
		return Plan{Kind: PlanNext, Next: n}
	}
	return Plan{Kind: PlanEnd}
}

// Verdict is the state of one condition.
type Verdict struct {
	// Decided is false while the window is open and nothing has settled it.
	Decided bool
	// Yes is the outcome once decided.
	Yes bool
	// At is when it was decided (the deciding event, or the deadline); while
	// undecided it is the deadline, the latest moment the verdict can settle.
	At time.Time
}

// Evaluate decides a condition from the earliest qualifying event.
//
// start is when the source step was sent and window how long the condition
// watches; firstEvent is the earliest event of the condition's signal at or
// before start+window (zero = none seen). The verdict is a pure function of
// those, so re-evaluating an enrollment later can never change a decided answer:
// events after the window are ignored, and once the window has closed no new
// event can land inside it. That is what lets the send path re-derive the route
// on every advance instead of storing it.
//
//   - A positive condition (opened/clicked/replied) is decided YES the moment the
//     event happens, and NO when the window closes without it.
//   - A negated one (not_opened/not_replied) is decided NO the moment the event
//     happens, and YES when the window closes without it.
//
// A caller that passes an event after the deadline gets the no-event answer.
func Evaluate(c Condition, start time.Time, window time.Duration, firstEvent, now time.Time) Verdict {
	if c == Always {
		return Verdict{Decided: true, Yes: true, At: start}
	}
	deadline := start.Add(window)
	if !firstEvent.IsZero() && !firstEvent.After(deadline) {
		at := firstEvent
		if at.Before(start) {
			at = start
		}
		return Verdict{Decided: true, Yes: !c.negated(), At: at}
	}
	if !now.Before(deadline) {
		return Verdict{Decided: true, Yes: c.negated(), At: deadline}
	}
	return Verdict{At: deadline}
}
