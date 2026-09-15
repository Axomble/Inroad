// Package fleetdecision is the vocabulary and the typed writer for the fleet
// decision log — the append-only record of every automated placement,
// quarantine, rotation and refusal.
//
// It exists because placement currently leaves no trace. A
// mailbox_worker_assignments row records the ANSWER and never the reasoning, so
// a mailbox that moved, and one that was refused a placement entirely, and one
// nothing ever considered, are indistinguishable afterwards. "Why is this
// mailbox on this worker?" is the first question an operator asks about a fleet
// and the one nothing could answer.
//
// The package's whole job is to stop call sites hand-building that prose, for
// one specific reason learned from a peer system: AN ENTRY MUST NOT PRINT A
// SCORE COMPARISON IT DID NOT ACTUALLY MAKE. A decision that was FORCED — the
// incumbent went unhealthy, there was one candidate, there were none — was never
// scored against anything, and a line reading "0.82 over 0.00" invents a runner-up
// that never existed. An operator then tunes a threshold against a number
// nothing computed. A log that lies is worse than no log, so the rule is
// enforced by construction here rather than by convention at each call site:
// Reason has no exported field and no way to be built except through the three
// constructors, and the only one that renders a comparison is the one that
// REQUIRES a real runner-up (see Chose, which degrades rather than inventing).
package fleetdecision

import (
	"errors"
	"fmt"
)

// Kind is what was decided. The set is CLOSED and mirrored by a CHECK
// constraint on fleet_decisions.kind.
type Kind string

const (
	// KindAssign: a mailbox was placed on a worker.
	KindAssign Kind = "assign"
	// KindRotate: a mailbox was moved from one worker to another.
	KindRotate Kind = "rotate"
	// KindQuarantine: a worker was taken out of service for new placements.
	KindQuarantine Kind = "quarantine"
	// KindRefused: a placement was DECLINED. Strict risk-band segregation
	// refusing to co-locate degraded traffic with clean is a decision, and
	// before this log it was invisible — the warmup sweep counted it as an
	// anonymous failure and moved on.
	KindRefused Kind = "refused"
)

// Known reports whether k is in the persisted vocabulary. A value outside it
// fails the table's CHECK, so callers validate before writing rather than
// discovering it as a constraint violation.
func (k Kind) Known() bool {
	switch k {
	case KindAssign, KindRotate, KindQuarantine, KindRefused:
		return true
	default:
		return false
	}
}

// Actor names who decided: "auto:<kind>" for the system, "operator:<user id>"
// for a human. Build one with Auto or Operator — the zero Actor is rejected by
// Entry.Validate, so an entry can never be attributed to nobody.
type Actor string

// Auto attributes a decision to the automation that made it.
func Auto(k Kind) Actor { return Actor("auto:" + k) }

// Operator attributes a decision to a human, by user id. An empty id yields the
// zero Actor rather than the bare string "operator:", which would name nobody
// and be indistinguishable from a bug in the audit trail.
func Operator(userID string) Actor {
	if userID == "" {
		return ""
	}
	return Actor("operator:" + userID)
}

// Candidate is one worker that was scored during a placement. A zero Candidate
// means "there was no such candidate" — Chose relies on that to refuse to render
// a comparison against one.
type Candidate struct {
	WorkerID string
	Score    float64
}

// Reason is operator-readable prose explaining a decision. It has no exported
// field on purpose: the only way to obtain a valid one is through Chose,
// ChoseUncontested or Forced, which is what makes "never print a comparison you
// did not make" a property of the type rather than a rule people remember.
type Reason struct{ text string }

func (r Reason) String() string { return r.text }

// Valid reports whether this Reason came from a constructor. The zero value is
// invalid, so an Entry literal that omits Reason is rejected rather than
// persisted as an empty explanation.
func (r Reason) Valid() bool { return r.text != "" }

// Chose is the reason for a placement made by COMPARING scored candidates. It is
// the only constructor that renders a comparison, and it renders one only when
// there was genuinely something to compare against: a zero runnerUp (a caller
// that reached for this constructor without a real second candidate) degrades to
// ChoseUncontested rather than publishing a comparison against an invented 0.00.
func Chose(winner, runnerUp Candidate, considered int) Reason {
	if runnerUp.WorkerID == "" {
		return ChoseUncontested(winner, considered)
	}
	return Reason{text: fmt.Sprintf("chose %s (score %.2f) over %s (score %.2f), %s considered",
		winner.WorkerID, winner.Score, runnerUp.WorkerID, runnerUp.Score, candidateCount(considered))}
}

// ChoseUncontested is the reason for a placement where the winner WAS scored but
// nothing else was eligible to be scored against it. It names the winner's score
// and no comparison, which is the honest shape of that decision.
func ChoseUncontested(winner Candidate, considered int) Reason {
	return Reason{text: fmt.Sprintf("chose %s (score %.2f), the only eligible candidate of %s considered",
		winner.WorkerID, winner.Score, candidateCount(considered))}
}

// Forced is the reason for a decision that involved no scoring at all — the
// incumbent stopped heartbeating, the fleet has one worker so there is no choice
// to make, the mailbox's risk band has no capacity. It renders the cause and
// NOTHING NUMERIC, because nothing numeric was computed
// (TestForcedReasonPrintsNoScore).
//
// cause is prose an operator can act on, written by the call site: it is the one
// thing this package cannot know. An empty cause yields the zero Reason, which
// Validate rejects, so a decision can never be recorded as "forced, no idea why".
func Forced(cause string) Reason {
	if cause == "" {
		return Reason{}
	}
	return Reason{text: "forced: " + cause}
}

// candidateCount renders the pool size without ever producing a decimal, so the
// "no score-shaped number in a forced reason" rule cannot be broken by the count
// leaking a float. A non-positive count is reported as unknown rather than as
// "0 candidates", which would itself be a claim nobody measured.
func candidateCount(n int) string {
	if n <= 0 {
		return "an unrecorded number of candidates"
	}
	if n == 1 {
		return "1 candidate"
	}
	return fmt.Sprintf("%d candidates", n)
}

// Entry is one decision, ready to append. Ids are strings at this seam like
// every other id crossing into coreapi; the implementation parses and pins them.
//
// WorkerID is empty when the decision names no destination — a refusal has none,
// and saying so is more honest than naming the worker that was rejected.
// MailboxID and WorkspaceID travel TOGETHER or not at all: a mailbox-scoped
// decision is tenant data and carries its tenant, a fleet-scoped one (quarantine
// this worker) carries neither. Validate enforces that pairing here so the
// table's CHECK is a backstop rather than the error message.
type Entry struct {
	Kind        Kind
	WorkerID    string
	MailboxID   string
	WorkspaceID string
	Reason      Reason
	TriggeredBy Actor
}

// ErrInvalidEntry marks an entry the decision log will never accept.
var ErrInvalidEntry = errors.New("fleetdecision: invalid entry")

// Validate reports why an entry cannot be written, in a sentence, rather than
// letting it fail as a constraint violation three layers down.
func (e Entry) Validate() error {
	switch {
	case !e.Kind.Known():
		return fmt.Errorf("%w: kind %q is not one of assign/rotate/quarantine/refused", ErrInvalidEntry, e.Kind)
	case !e.Reason.Valid():
		return fmt.Errorf("%w: reason is empty; build one with Chose, ChoseUncontested or Forced", ErrInvalidEntry)
	case e.TriggeredBy == "":
		return fmt.Errorf("%w: triggered_by is empty; build one with Auto or Operator", ErrInvalidEntry)
	case (e.MailboxID == "") != (e.WorkspaceID == ""):
		return fmt.Errorf("%w: a mailbox-scoped decision must carry its workspace and a fleet-scoped one must carry neither (got mailbox %q, workspace %q)",
			ErrInvalidEntry, e.MailboxID, e.WorkspaceID)
	default:
		return nil
	}
}
