package inprocess

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/app/sequencestep"
	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/cadence"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/seqgraph"
)

// Conditional branching on the send path.
//
// # When this code runs
//
// ONLY for a campaign that has at least one branch, or for an enrollment whose
// current wait was set by a condition (awaiting_condition_step == current_step:
// the campaign's last branch was deleted while this contact waited on it). Every
// other enrollment — every linear campaign — takes the original GetNextStep path
// in localStepSendJob, untouched. See usesGraphRouting.
//
// # The route is re-derived, never stored
//
// An enrollment carries no "which branch did I take" state. On every advance the
// route out of the step it last received is recomputed from the CURRENT graph
// and the engagement evidence. That is sound because a verdict is a pure
// function of events inside a fixed window (seqgraph.Evaluate): once decided it
// cannot change, so re-deriving it later lands on the same exit.
//
// # Mid-flight edit rule
//
// Graph edits are allowed on a running campaign, and the rule for an enrollment
// already in flight is: EVERY DECISION IS MADE AGAINST THE GRAPH AS IT IS WHEN
// THE DECISION IS MADE. Concretely, for an enrollment whose last received step
// is S:
//
//   - A message already sent is never affected; nothing is re-sent or recalled.
//   - A branch added to S after S was sent applies at the next advance, with its
//     window measured from S's send (last_sent_at) — so a window that has
//     already elapsed decides immediately.
//   - A branch on S whose condition, window or exits change while the contact
//     waits is evaluated with the NEW definition at the next check. That check
//     is at most conditionRecheckInterval away while the condition is OPEN, but
//     can be later: rechecks are snapped into the campaign's send window (a
//     Friday-evening recheck lands on Monday morning), an out-of-office deferral
//     that ends later wins, and once a route is decided the next check is when
//     its target falls due — which may be days out. A lengthened window keeps
//     waiting; a shortened one decides at the next check. Until the target is
//     actually SENT, a decided route is re-derived too, so changing S's exits
//     in that interval re-routes the contact to the new exit.
//   - Known edge: the recover-forward window. If S's routed target T was
//     delivered but the cursor advance to T failed, and S's exit is changed
//     BEFORE the retry runs, the retry routes to the NEW target U and sends it
//     too — the contact receives both T and U. The window is one failed
//     transaction plus an asynq retry (seconds); closing it would mean storing
//     the route, which the design deliberately does not do.
//   - A branch removed from S while the contact waits returns S to linear
//     fall-through, and the successor still waits out its own delay from S's
//     send (awaiting_condition_step is what keeps this true once the campaign
//     has no branches left).
//   - An exit whose target step is deleted becomes an end (the FK nulls it).
//     Step deletes are draft-only, so this cannot happen to a running campaign.
//   - Once the route out of S has been decided AND its target sent, the
//     enrollment's cursor is on the target; later edits to S no longer matter.
const (
	// conditionRecheckInterval bounds how long an undecided condition goes
	// unobserved. The route is taken the first time the advance runs after the
	// deciding event, and the routed step's delay is measured from the event
	// itself, so this only adds latency when that delay is shorter than the
	// interval. A reply additionally nudges the enrollment (see
	// NudgeEnrollmentAwaitingReply), so the one signal an operator expects to act
	// on promptly does not wait for the interval.
	conditionRecheckInterval = time.Hour
	// routeDueTolerance absorbs the skew between the worker clock that scheduled
	// an advance and the database clock that stamped last_sent_at, so an advance
	// that fires on time is not bounced for being a few seconds early.
	routeDueTolerance = time.Minute
)

// usesGraphRouting reports whether an enrollment must be routed through the
// branch graph rather than the linear GetNextStep path.
func usesGraphRouting(branches []gen.SequenceStepBranch, b gen.GetStepEnrollmentBundleRow) bool {
	return len(branches) > 0 ||
		(b.AwaitingConditionStep != nil && *b.AwaitingConditionStep == b.CurrentStep && b.CurrentStep > 0)
}

// routeKind is what the send path does with this advance.
type routeKind int

const (
	// routeSend: send target now.
	routeSend routeKind = iota
	// routeWait: nothing to send yet; look again at recheckAt.
	routeWait
	// routeEnd: the path has ended; complete the enrollment without a send.
	routeEnd
	// routeSkip: the campaign has no step to send at all (the linear path's
	// ErrNoRows → Skip, preserved).
	routeSkip
)

// routeDecision is decideRoute's answer.
type routeDecision struct {
	kind      routeKind
	target    seqgraph.Step
	recheckAt time.Time
	// lastStep and nextDelay describe the route OUT of target once it is sent —
	// what AdvanceStepCursor schedules next. They replace the linear path's
	// "is there a step after this one" lookup.
	lastStep  bool
	nextDelay int
}

// routeInput is everything decideRoute reads. It is a value, and evidence is a
// function, so the whole decision is unit-testable without a database.
type routeInput struct {
	graph seqgraph.Graph
	// cursor is sequence_enrollments.current_step: the step_order last sent, 0
	// before the first send.
	cursor int32
	// lastSentAt is when the cursor step was sent — the start of its window and
	// the reference point for the successor's delay.
	lastSentAt time.Time
	now        time.Time
	window     cadence.Window
	// key seeds the window's humanization (the enrollment id), as it does for
	// every other due-time computation on this path.
	key string
}

// evidenceFunc returns the earliest event of the branch's signal for the cursor
// step inside [start, start+window], or the zero time when there is none.
type evidenceFunc func(cursor seqgraph.Step, b seqgraph.Branch, start time.Time) (time.Time, error)

// decideRoute routes one advance. See the package-level notes above for the
// rules; the order of the checks is:
//
//  1. No send yet → the entry step, now (the launch stagger scheduled it).
//  2. The route out of the cursor step: end, a fixed next step, or a condition.
//  3. A condition still open → wait until it can next change (bounded).
//  4. A decided route → wait until the target's delay has elapsed from the
//     decision AND the campaign's window is open; then send.
func decideRoute(in routeInput, evidence evidenceFunc) (routeDecision, error) {
	if in.cursor == 0 {
		entry, ok := in.graph.Entry()
		if !ok {
			return routeDecision{kind: routeSkip}, nil
		}
		return in.sendDecision(entry), nil
	}
	if in.lastSentAt.IsZero() {
		// current_step > 0 is only ever written together with last_sent_at, so
		// this is a corrupted row. Failing loudly beats inventing a window start:
		// a start of "now" would re-open the window on every advance and the
		// condition would never close.
		return routeDecision{}, fmt.Errorf("enrollment %s is past step %d but has no last_sent_at", in.key, in.cursor)
	}
	cursor, ok := in.graph.StepByOrder(in.cursor)
	if !ok {
		// The step this contact last received no longer exists. Step deletes are
		// draft-only, so a running campaign cannot get here; ending the path is
		// the answer that cannot send the wrong message.
		return routeDecision{kind: routeEnd}, nil
	}

	plan := in.graph.After(cursor.ID)
	decidedAt := in.lastSentAt
	var target seqgraph.Step
	switch plan.Kind {
	case seqgraph.PlanEnd:
		return routeDecision{kind: routeEnd}, nil
	case seqgraph.PlanNext:
		target = plan.Next
	case seqgraph.PlanAwait:
		first, err := evidence(cursor, plan.Branch, in.lastSentAt)
		if err != nil {
			return routeDecision{}, err
		}
		verdict := seqgraph.Evaluate(plan.Branch.Condition, in.lastSentAt, plan.Branch.Window(), first, in.now)
		if !verdict.Decided {
			// Look again when the answer can next change: the deadline, or the
			// recheck interval if that comes first.
			return routeDecision{kind: routeWait, recheckAt: in.snap(minTime(verdict.At, in.now.Add(conditionRecheckInterval)))}, nil
		}
		next, ok := in.graph.Step(plan.Branch.Target(verdict))
		if !ok {
			return routeDecision{kind: routeEnd}, nil
		}
		target, decidedAt = next, verdict.At
	}

	due := decidedAt.Add(time.Duration(target.DelaySeconds) * time.Second)
	if in.now.Before(due.Add(-routeDueTolerance)) || !in.window.Contains(in.now) {
		return routeDecision{kind: routeWait, recheckAt: in.snap(maxTime(due, in.now))}, nil
	}
	return in.sendDecision(target), nil
}

// sendDecision is a routeSend for target, with the route out of it resolved.
func (in routeInput) sendDecision(target seqgraph.Step) routeDecision {
	d := routeDecision{kind: routeSend, target: target}
	switch plan := in.graph.After(target.ID); plan.Kind {
	case seqgraph.PlanEnd:
		d.lastStep = true
	case seqgraph.PlanNext:
		d.nextDelay = int(plan.Next.DelaySeconds)
	case seqgraph.PlanAwait:
		// The first look at the condition: the deadline, or one recheck interval
		// after the send if that is sooner.
		d.nextDelay = int(min(plan.Branch.Window(), conditionRecheckInterval) / time.Second)
	}
	return d
}

// snap moves t into the campaign's send window. A wait must END inside the
// window: the advance that wakes up may send immediately, and nothing later on
// this path re-checks the window.
func (in routeInput) snap(t time.Time) time.Time {
	if at, err := in.window.Next(t, in.key); err == nil {
		return at
	}
	return t
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// graphRouteResult is routeGraph's answer: the decision plus the full step row
// to send when there is one.
type graphRouteResult struct {
	routeDecision
	step gen.SequenceStep
}

// routeGraph loads the campaign's graph and routes one advance through it. It
// applies the database-backed half of the rules decideRoute cannot see: the
// engagement evidence and the loop backstop.
func (c client) routeGraph(ctx context.Context, ws uuid.UUID, enrollmentID string, b gen.GetStepEnrollmentBundleRow,
	branches []gen.SequenceStepBranch, sched cadence.Schedule, now time.Time) (graphRouteResult, error) {
	steps, err := c.q.ListStepsByCampaign(ctx, gen.ListStepsByCampaignParams{CampaignID: b.CampaignID, WorkspaceID: ws})
	if err != nil {
		return graphRouteResult{}, fmt.Errorf("list steps: %w", err)
	}
	win, err := sched.Compile()
	if err != nil {
		return graphRouteResult{}, fmt.Errorf("enrollment %s route: %w", enrollmentID, err)
	}
	g := sequencestep.BuildGraph(steps, branches)
	d, err := decideRoute(routeInput{
		graph: g, cursor: b.CurrentStep, lastSentAt: b.LastSentAt.Time, now: now, window: win, key: enrollmentID,
	}, func(cursor seqgraph.Step, br seqgraph.Branch, start time.Time) (time.Time, error) {
		return c.firstEvidence(ctx, ws, b, cursor, br, start)
	})
	if err != nil {
		return graphRouteResult{}, err
	}
	if d.kind != routeSend {
		return graphRouteResult{routeDecision: d}, nil
	}
	if b.CurrentStep > 0 && g.HasBranches() {
		revisit, err := c.revisits(ctx, ws, b, b.CurrentStep, d.target)
		if err != nil {
			return graphRouteResult{}, err
		}
		if revisit {
			// The save path refuses every loop, under a lock, so this is a
			// backstop: without it a loop that got in anyway would recover-forward
			// through ClaimAlreadySent step after step, forever, never sending
			// and never finishing. End the path loudly instead.
			slog.WarnContext(ctx, "sequence_graph_loop_detected",
				"enrollment_id", enrollmentID, "campaign_id", b.CampaignID, "step_order", d.target.Order)
			return graphRouteResult{routeDecision: routeDecision{kind: routeEnd}}, nil
		}
	}
	for _, st := range steps {
		if st.ID == d.target.ID {
			return graphRouteResult{routeDecision: d, step: st}, nil
		}
	}
	// Unreachable: target came from this same step list.
	return graphRouteResult{}, fmt.Errorf("routed step %s vanished", d.target.ID)
}

// revisits reports whether target was already sent EARLIER on this contact's
// path. Its deterministic send row existing is not enough — that is also the
// recover-forward case, where target was sent but the cursor advance to it did
// not commit — so the test compares it with the CURRENT step's own send row: a
// recover-forward row for target was created after the current step's, a loop's
// row before it (the loop visited target first).
//
// Both instants are sends.created_at, the same column stamped by the same
// database clock, so there is no skew to absorb and no tolerance — a tolerance
// would blind this to the loop that matters most, zero-delay steps seconds
// apart. (enrollment.last_sent_at would be the wrong reference: a recover-forward
// re-stamps it, so it can land after a genuinely-earlier visit.)
func (c client) revisits(ctx context.Context, ws uuid.UUID, b gen.GetStepEnrollmentBundleRow, cursorOrder int32, target seqgraph.Step) (bool, error) {
	targetAt, found, err := c.stepSendCreatedAt(ctx, ws, b, target.Order)
	if err != nil || !found {
		return false, err
	}
	cursorAt, found, err := c.stepSendCreatedAt(ctx, ws, b, cursorOrder)
	if err != nil || !found {
		// No row for the step the contact is on cannot happen past step 0 (the
		// cursor only moves after a claim); with nothing to compare against,
		// the claim remains the delivery guard.
		return false, err
	}
	return targetAt.Before(cursorAt), nil
}

// stepSendCreatedAt reads when one step's deterministic send row was created;
// found=false when the row does not exist.
func (c client) stepSendCreatedAt(ctx context.Context, ws uuid.UUID, b gen.GetStepEnrollmentBundleRow, order int32) (time.Time, bool, error) {
	created, err := c.q.StepSendCreatedAt(ctx, gen.StepSendCreatedAtParams{
		ID: deriveStepSendID(b.CampaignID, b.ContactID, int(order)), WorkspaceID: ws,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("step send lookup: %w", err)
	}
	return created.Time, true, nil
}

// firstEvidence reads the earliest qualifying event for one condition.
//
// Opens and clicks are keyed on the cursor step's OWN deterministic send id, so
// they are "opened THIS step", never an earlier one; they count HUMAN events
// only (invariant 86). Replies span both legs of the conversation: the step went
// out as a sends row, but the answer lives in inbox_messages, matched on the
// enrollment's campaign and contact rather than on any one send.
func (c client) firstEvidence(ctx context.Context, ws uuid.UUID, b gen.GetStepEnrollmentBundleRow,
	cursor seqgraph.Step, br seqgraph.Branch, start time.Time) (time.Time, error) {
	end := start.Add(br.Window())
	var (
		at  pgtype.Timestamptz
		err error
	)
	switch br.Condition.Signal() {
	case seqgraph.SignalOpen, seqgraph.SignalClick:
		kind := gen.TrackingEventKindOpen
		if br.Condition.Signal() == seqgraph.SignalClick {
			kind = gen.TrackingEventKindClick
		}
		at, err = c.q.FirstHumanTrackingEventAt(ctx, gen.FirstHumanTrackingEventAtParams{
			SendID:      deriveStepSendID(b.CampaignID, b.ContactID, int(cursor.Order)),
			WorkspaceID: ws, Kind: kind, WindowEnd: pgtype.Timestamptz{Time: end, Valid: true},
		})
	case seqgraph.SignalReply:
		at, err = c.q.FirstInboundReplyAt(ctx, gen.FirstInboundReplyAtParams{
			WorkspaceID: ws,
			CampaignID:  pgtype.UUID{Bytes: b.CampaignID, Valid: true},
			ContactID:   pgtype.UUID{Bytes: b.ContactID, Valid: true},
			WindowStart: pgtype.Timestamptz{Time: start, Valid: true},
			WindowEnd:   pgtype.Timestamptz{Time: end, Valid: true},
			LabelKey:    br.ReplyLabelKey,
		})
	default:
		return time.Time{}, fmt.Errorf("condition %q has no evidence to read", br.Condition)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("read %s evidence: %w", br.Condition, err)
	}
	return at.Time, nil
}

// routeStep loads the schedule the route needs (a wait must end inside the send
// window) and routes the advance.
func (c client) routeStep(ctx context.Context, ws uuid.UUID, enrollmentID string, b gen.GetStepEnrollmentBundleRow,
	branches []gen.SequenceStepBranch) (graphRouteResult, error) {
	sched, err := c.loadSchedule(ctx, ws, b.CampaignID, b.Timezone)
	if err != nil {
		return graphRouteResult{}, err
	}
	return c.routeGraph(ctx, ws, enrollmentID, b, branches, sched, time.Now())
}

// applyRoute carries out a route that sends nothing this advance, and returns
// the job the worker acts on.
//
// These are control-plane writes made while building a job, which the linear
// path does not do. They belong here rather than behind new worker-invoked
// coreapi methods because they are DECISIONS about committed data, not outcomes
// of a delivery: both are idempotent and guarded on status='active', a retried
// or raced advance recomputes the identical decision (the verdict cannot change
// once taken), and the remote transport serves this very function in the
// control plane, so a worker never needs a way to express either write.
func (c client) applyRoute(ctx context.Context, ws, eid uuid.UUID, enrollmentID string,
	b gen.GetStepEnrollmentBundleRow, d routeDecision) (coreapi.StepSendJob, error) {
	switch d.kind {
	case routeEnd:
		if err := c.enroll.FinishRoute(ctx, ws, eid); err != nil {
			return coreapi.StepSendJob{}, fmt.Errorf("finish routed enrollment: %w", err)
		}
		return coreapi.StepSendJob{Skip: true}, nil
	case routeWait:
		// Never pull a due time EARLIER than one already stamped: a later
		// next_due_at is an out-of-office deferral, and waking inside the stated
		// absence would route — and possibly send — into it.
		recheck := d.recheckAt
		if b.NextDueAt.Valid && b.NextDueAt.Time.After(recheck) {
			recheck = b.NextDueAt.Time
		}
		if err := c.enroll.AwaitCondition(ctx, ws, eid, recheck); err != nil {
			return coreapi.StepSendJob{}, fmt.Errorf("await branch condition: %w", err)
		}
		return coreapi.StepSendJob{
			EnrollmentID: enrollmentID, WorkspaceID: ws.String(),
			ConditionPending: true, RecheckAt: recheck,
			// Skip rides along so a worker built before ConditionPending existed
			// treats this job as inert instead of claiming a send with an empty
			// job; the next_due_at stamped above lets the sweeper re-drive it.
			Skip: true,
		}, nil
	default:
		return coreapi.StepSendJob{Skip: true}, nil
	}
}
