package inprocess

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/cadence"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/seqgraph"
)

// alwaysOpen is a window that never blocks, so a test isolates the rule it is
// about from send-window snapping.
func alwaysOpen(t *testing.T) cadence.Window {
	t.Helper()
	var ws []cadence.SendWindow
	for d := range 7 {
		ws = append(ws, cadence.SendWindow{Weekday: d, StartMinute: 0, EndMinute: 24 * 60})
	}
	w, err := cadence.Schedule{Timezone: "UTC", Windows: ws}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// businessHours is Mon–Fri 09:00–17:00 UTC.
func businessHours(t *testing.T) cadence.Window {
	t.Helper()
	w, err := cadence.DefaultSchedule("UTC").Compile()
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// routeSteps is a three-step campaign; step 3 has a 1h delay, step 2 a 2h one.
func routeSteps() (ids [3]uuid.UUID, steps []seqgraph.Step) {
	for i := range ids {
		ids[i] = uuid.New()
	}
	return ids, []seqgraph.Step{
		{ID: ids[0], Order: 1},
		{ID: ids[1], Order: 2, DelaySeconds: 7200},
		{ID: ids[2], Order: 3, DelaySeconds: 3600},
	}
}

// noEvidence is an evidence source that has seen nothing.
func noEvidence(seqgraph.Step, seqgraph.Branch, time.Time) (time.Time, error) {
	return time.Time{}, nil
}

// eventAt is an evidence source that saw the signal at t.
func eventAt(t time.Time) evidenceFunc {
	return func(seqgraph.Step, seqgraph.Branch, time.Time) (time.Time, error) { return t, nil }
}

// A Wednesday, mid-morning UTC — inside businessHours.
var routeSentAt = time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)

func TestDecideRouteEntryStepSendsNow(t *testing.T) {
	ids, steps := routeSteps()
	g := seqgraph.New(steps, []seqgraph.Branch{{StepID: ids[0], Condition: seqgraph.Opened, WithinDays: 2, Yes: ids[2], No: ids[1]}})
	d, err := decideRoute(routeInput{graph: g, cursor: 0, now: routeSentAt, window: alwaysOpen(t), key: "e"}, noEvidence)
	if err != nil {
		t.Fatal(err)
	}
	if d.kind != routeSend || d.target.ID != ids[0] {
		t.Fatalf("entry = %+v", d)
	}
	// The first look at step 1's condition is one recheck interval out (the
	// 2-day window is longer), and the enrollment is not on its last step.
	if d.lastStep || d.nextDelay != int(conditionRecheckInterval/time.Second) {
		t.Fatalf("after step 1: lastStep=%v nextDelay=%d", d.lastStep, d.nextDelay)
	}
}

func TestDecideRouteNoStepsSkips(t *testing.T) {
	d, err := decideRoute(routeInput{graph: seqgraph.New(nil, nil), now: routeSentAt, window: alwaysOpen(t)}, noEvidence)
	if err != nil || d.kind != routeSkip {
		t.Fatalf("empty campaign = %+v, %v", d, err)
	}
}

func TestDecideRouteConditionPendingWaitsBoundedByRecheck(t *testing.T) {
	ids, steps := routeSteps()
	g := seqgraph.New(steps, []seqgraph.Branch{{StepID: ids[0], Condition: seqgraph.Opened, WithinDays: 2, Yes: ids[2], No: ids[1]}})
	now := routeSentAt.Add(10 * time.Minute)
	d, err := decideRoute(routeInput{graph: g, cursor: 1, lastSentAt: routeSentAt, now: now, window: alwaysOpen(t), key: "e"}, noEvidence)
	if err != nil {
		t.Fatal(err)
	}
	if d.kind != routeWait {
		t.Fatalf("want wait, got %+v", d)
	}
	// Snapped into the window, which only ever moves an instant later, by under
	// two minutes of humanization.
	want := now.Add(conditionRecheckInterval)
	if d.recheckAt.Before(want) || d.recheckAt.After(want.Add(2*time.Minute)) {
		t.Fatalf("recheck = %v, want ~%v", d.recheckAt, want)
	}
}

// Near the deadline the recheck is the deadline, not a full interval later.
func TestDecideRouteConditionPendingRecheckStopsAtDeadline(t *testing.T) {
	ids, steps := routeSteps()
	g := seqgraph.New(steps, []seqgraph.Branch{{StepID: ids[0], Condition: seqgraph.Replied, WithinDays: 1, Yes: ids[2], No: ids[1]}})
	deadline := routeSentAt.Add(24 * time.Hour)
	now := deadline.Add(-10 * time.Minute)
	d, err := decideRoute(routeInput{graph: g, cursor: 1, lastSentAt: routeSentAt, now: now, window: alwaysOpen(t), key: "e"}, noEvidence)
	if err != nil {
		t.Fatal(err)
	}
	if d.kind != routeWait || d.recheckAt.Before(deadline) || d.recheckAt.After(deadline.Add(2*time.Minute)) {
		t.Fatalf("recheck = %+v, want ~deadline %v", d, deadline)
	}
}

// The positive event routes YES as soon as it is seen, and the target's delay
// counts from the event — not from the send, and not from when we noticed.
func TestDecideRouteEarlyEventRoutesYesWithDelayFromEvent(t *testing.T) {
	ids, steps := routeSteps()
	g := seqgraph.New(steps, []seqgraph.Branch{{StepID: ids[0], Condition: seqgraph.Opened, WithinDays: 3, Yes: ids[2], No: ids[1]}})
	opened := routeSentAt.Add(30 * time.Minute)
	in := routeInput{graph: g, cursor: 1, lastSentAt: routeSentAt, window: alwaysOpen(t), key: "e"}

	// 40 minutes after the open: step 3's 1h delay has not elapsed yet.
	in.now = opened.Add(40 * time.Minute)
	d, err := decideRoute(in, eventAt(opened))
	if err != nil {
		t.Fatal(err)
	}
	due := opened.Add(time.Hour)
	if d.kind != routeWait || d.recheckAt.Before(due) || d.recheckAt.After(due.Add(2*time.Minute)) {
		t.Fatalf("before the yes step is due: %+v, want wait until ~%v", d, due)
	}

	// Once due: send step 3 (the YES exit). Step 3 has no branch and is last.
	in.now = due.Add(time.Minute)
	d, err = decideRoute(in, eventAt(opened))
	if err != nil {
		t.Fatal(err)
	}
	if d.kind != routeSend || d.target.ID != ids[2] || !d.lastStep {
		t.Fatalf("yes route = %+v", d)
	}
}

func TestDecideRouteWindowCloseRoutesNo(t *testing.T) {
	ids, steps := routeSteps()
	g := seqgraph.New(steps, []seqgraph.Branch{{StepID: ids[0], Condition: seqgraph.Clicked, WithinDays: 1, Yes: ids[2], No: ids[1]}})
	// Step 2's delay (2h) counts from the deadline.
	now := routeSentAt.Add(24*time.Hour + 2*time.Hour + time.Minute)
	d, err := decideRoute(routeInput{graph: g, cursor: 1, lastSentAt: routeSentAt, now: now, window: alwaysOpen(t), key: "e"}, noEvidence)
	if err != nil {
		t.Fatal(err)
	}
	if d.kind != routeSend || d.target.ID != ids[1] {
		t.Fatalf("no route = %+v", d)
	}
	// Step 2 has no branch: it falls through to step 3 with step 3's delay.
	if d.lastStep || d.nextDelay != 3600 {
		t.Fatalf("after step 2: lastStep=%v nextDelay=%d", d.lastStep, d.nextDelay)
	}
}

// A negated condition is decided NO by the event, immediately.
func TestDecideRouteNotRepliedDecidedByReply(t *testing.T) {
	ids, steps := routeSteps()
	g := seqgraph.New(steps, []seqgraph.Branch{{StepID: ids[0], Condition: seqgraph.NotReplied, WithinDays: 5, Yes: ids[1]}})
	replied := routeSentAt.Add(time.Hour)
	d, err := decideRoute(routeInput{graph: g, cursor: 1, lastSentAt: routeSentAt, now: replied.Add(time.Minute),
		window: alwaysOpen(t), key: "e"}, eventAt(replied))
	if err != nil {
		t.Fatal(err)
	}
	// The NO exit is unset: the path ends, without waiting out the 5 days.
	if d.kind != routeEnd {
		t.Fatalf("replied on a not_replied branch with no NO exit = %+v", d)
	}
}

func TestDecideRouteAlwaysToEndFinishes(t *testing.T) {
	ids, steps := routeSteps()
	g := seqgraph.New(steps, []seqgraph.Branch{{StepID: ids[1], Condition: seqgraph.Always}})
	d, err := decideRoute(routeInput{graph: g, cursor: 2, lastSentAt: routeSentAt, now: routeSentAt.Add(time.Hour),
		window: alwaysOpen(t), key: "e"}, noEvidence)
	if err != nil || d.kind != routeEnd {
		t.Fatalf("always -> end = %+v, %v", d, err)
	}
}

// A decided route may not send outside the campaign's window: it waits for the
// window to open, even though the target is overdue.
func TestDecideRouteWaitsForWindow(t *testing.T) {
	ids, steps := routeSteps()
	g := seqgraph.New(steps, []seqgraph.Branch{{StepID: ids[0], Condition: seqgraph.Opened, WithinDays: 1, Yes: ids[2], No: ids[1]}})
	opened := routeSentAt.Add(time.Hour)
	saturday := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	d, err := decideRoute(routeInput{graph: g, cursor: 1, lastSentAt: routeSentAt, now: saturday,
		window: businessHours(t), key: "e"}, eventAt(opened))
	if err != nil {
		t.Fatal(err)
	}
	monday := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	if d.kind != routeWait || d.recheckAt.Before(monday) || d.recheckAt.After(monday.Add(2*time.Minute)) {
		t.Fatalf("weekend route = %+v, want wait until ~%v", d, monday)
	}
}

// The mid-flight rule for a DELETED branch: the enrollment falls through to the
// linear successor, which still honours its own delay from the last send rather
// than going out at the recheck time the condition had scheduled.
func TestDecideRouteBranchRemovedMidWaitHonoursDelay(t *testing.T) {
	_, steps := routeSteps()
	g := seqgraph.New(steps, nil) // the only branch was deleted
	in := routeInput{graph: g, cursor: 1, lastSentAt: routeSentAt, window: alwaysOpen(t), key: "e"}

	in.now = routeSentAt.Add(time.Hour) // a recheck the old condition scheduled
	d, err := decideRoute(in, noEvidence)
	if err != nil {
		t.Fatal(err)
	}
	due := routeSentAt.Add(2 * time.Hour) // step 2's delay
	if d.kind != routeWait || d.recheckAt.Before(due) {
		t.Fatalf("fall-through before its delay = %+v, want wait until %v", d, due)
	}

	in.now = due
	if d, err = decideRoute(in, noEvidence); err != nil || d.kind != routeSend || d.target.Order != 2 {
		t.Fatalf("fall-through once due = %+v, %v", d, err)
	}
}

// An advance that fires on schedule is never bounced by the few seconds of skew
// between the worker's clock and the database's last_sent_at.
func TestDecideRouteToleratesClockSkew(t *testing.T) {
	ids, steps := routeSteps()
	g := seqgraph.New(steps, []seqgraph.Branch{{StepID: ids[0], Condition: seqgraph.Always, Yes: ids[1]}})
	due := routeSentAt.Add(2 * time.Hour)
	d, err := decideRoute(routeInput{graph: g, cursor: 1, lastSentAt: routeSentAt, now: due.Add(-5 * time.Second),
		window: alwaysOpen(t), key: "e"}, noEvidence)
	if err != nil || d.kind != routeSend {
		t.Fatalf("5s early = %+v, %v", d, err)
	}
}

func TestDecideRouteDeletedCursorStepEnds(t *testing.T) {
	_, steps := routeSteps()
	g := seqgraph.New(steps, nil)
	d, err := decideRoute(routeInput{graph: g, cursor: 9, lastSentAt: routeSentAt, now: routeSentAt, window: alwaysOpen(t)}, noEvidence)
	if err != nil || d.kind != routeEnd {
		t.Fatalf("cursor on a vanished step = %+v, %v", d, err)
	}
}

func TestDecideRouteMissingLastSentIsAnError(t *testing.T) {
	_, steps := routeSteps()
	_, err := decideRoute(routeInput{graph: seqgraph.New(steps, nil), cursor: 1, now: routeSentAt, window: alwaysOpen(t)}, noEvidence)
	if err == nil {
		t.Fatal("a cursor past step 0 with no last_sent_at must fail loudly")
	}
}

func TestDecideRouteEvidenceErrorPropagates(t *testing.T) {
	ids, steps := routeSteps()
	g := seqgraph.New(steps, []seqgraph.Branch{{StepID: ids[0], Condition: seqgraph.Opened, WithinDays: 1}})
	boom := errors.New("db down")
	_, err := decideRoute(routeInput{graph: g, cursor: 1, lastSentAt: routeSentAt, now: routeSentAt, window: alwaysOpen(t)},
		func(seqgraph.Step, seqgraph.Branch, time.Time) (time.Time, error) { return time.Time{}, boom })
	if !errors.Is(err, boom) {
		t.Fatalf("evidence error = %v, want %v", err, boom)
	}
}

// Linear campaigns never reach the graph router: no branches and no condition
// wait means usesGraphRouting is false and localStepSendJob runs the original
// GetNextStep path. That is the whole of the "linear campaigns are unchanged"
// guarantee on the send path, so it is pinned here explicitly.
func TestUsesGraphRoutingOnlyForBranchedOrParkedEnrollments(t *testing.T) {
	two, three := int32(2), int32(3)
	cases := []struct {
		name     string
		branches []gen.SequenceStepBranch
		bundle   gen.GetStepEnrollmentBundleRow
		want     bool
	}{
		{"linear, never waited", nil, gen.GetStepEnrollmentBundleRow{CurrentStep: 2}, false},
		{"linear, fresh enrollment", nil, gen.GetStepEnrollmentBundleRow{CurrentStep: 0}, false},
		{"linear, waited at an EARLIER step", nil, gen.GetStepEnrollmentBundleRow{CurrentStep: 3, AwaitingConditionStep: &two}, false},
		{"branch deleted while waiting here", nil, gen.GetStepEnrollmentBundleRow{CurrentStep: 3, AwaitingConditionStep: &three}, true},
		{"campaign has a branch", []gen.SequenceStepBranch{{Condition: "always"}}, gen.GetStepEnrollmentBundleRow{}, true},
	}
	for _, tc := range cases {
		if got := usesGraphRouting(tc.branches, tc.bundle); got != tc.want {
			t.Errorf("%s: usesGraphRouting = %v, want %v", tc.name, got, tc.want)
		}
	}
}
