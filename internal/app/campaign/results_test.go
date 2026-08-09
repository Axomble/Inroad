package campaign

import (
	"testing"

	"github.com/google/uuid"
)

var variantB = uuid.New()

func sendRow(step int32, variant uuid.UUID, sent, opens, clicks int64) SendResultRow {
	return SendResultRow{StepOrder: step, VariantID: variant, Sent: sent, Opens: opens, Clicks: clicks}
}

func replyRow(step int32, variant uuid.UUID, n int64) OutcomeResultRow {
	return OutcomeResultRow{StepOrder: step, VariantID: variant, StopReason: stopReasonReplied, Count: n}
}

func labels() map[uuid.UUID]VariantLabel {
	return map[uuid.UUID]VariantLabel{variantB: {Label: "B", Weight: 1}}
}

func onlyStep(t *testing.T, steps []StepResults) StepResults {
	t.Helper()
	if len(steps) != 1 {
		t.Fatalf("steps = %d, want 1", len(steps))
	}
	return steps[0]
}

// The base copy has no variant row, so it must still appear — as "A", first.
func TestBuildResultsNamesTheBaseCopyA(t *testing.T) {
	steps := buildResults(
		[]SendResultRow{sendRow(1, uuid.Nil, 100, 40, 10), sendRow(1, variantB, 100, 30, 5)},
		nil, labels(), map[int32]string{1: "quick question"},
	)
	step := onlyStep(t, steps)
	if step.Subject != "quick question" {
		t.Errorf("subject = %q, want the step's", step.Subject)
	}
	if len(step.Rows) != 2 || step.Rows[0].Label != "A" || !step.Rows[0].IsBase {
		t.Fatalf("rows = %+v, want the base copy first as A", step.Rows)
	}
	if step.Rows[1].Label != "B" {
		t.Errorf("second row = %q, want B", step.Rows[1].Label)
	}
}

// A variant deleted while it had no sends, whose in-flight sends then landed,
// must be reported under its id — folding it into the base would silently
// credit another arm with its numbers.
func TestBuildResultsKeepsAnUnknownVariantSeparate(t *testing.T) {
	ghost := uuid.New()
	steps := buildResults(
		[]SendResultRow{sendRow(1, uuid.Nil, 10, 0, 0), sendRow(1, ghost, 5, 0, 0)},
		nil, labels(), nil,
	)
	step := onlyStep(t, steps)
	if len(step.Rows) != 2 {
		t.Fatalf("rows = %d, want the unknown variant kept separate", len(step.Rows))
	}
	var found bool
	for _, r := range step.Rows {
		if !r.IsBase && r.Sent == 5 {
			found = true
			if r.Label == "A" {
				t.Error("an unknown variant must not be labelled as the base copy")
			}
		}
	}
	if !found {
		t.Error("the unknown variant's sends went missing")
	}
}

func TestBuildResultsDividesRatesBySends(t *testing.T) {
	steps := buildResults(
		[]SendResultRow{sendRow(1, uuid.Nil, 200, 50, 20)},
		[]OutcomeResultRow{replyRow(1, uuid.Nil, 10)},
		labels(), nil,
	)
	row := onlyStep(t, steps).Rows[0]
	if row.OpenRate != 0.25 || row.ClickRate != 0.10 || row.ReplyRate != 0.05 {
		t.Errorf("rates = open %v click %v reply %v, want 0.25 / 0.10 / 0.05",
			row.OpenRate, row.ClickRate, row.ReplyRate)
	}
}

// Zero sends must produce zeros, not NaN: a NaN serialises as null and renders
// as a blank cell that reads like missing data rather than "nothing sent yet".
func TestBuildResultsGuardsAgainstDivisionByZero(t *testing.T) {
	steps := buildResults([]SendResultRow{sendRow(1, uuid.Nil, 0, 0, 0)}, nil, labels(), nil)
	row := onlyStep(t, steps).Rows[0]
	if row.OpenRate != 0 || row.ReplyRate != 0 {
		t.Errorf("rates = %v / %v, want zeros", row.OpenRate, row.ReplyRate)
	}
}

// --- the winner rule -----------------------------------------------------
//
// Naming a winner is an instruction to promote one arm and retire another, so
// these are the tests that matter most: every case below is one where saying
// nothing is the correct answer.

func TestPickWinnerNamesAClearLeader(t *testing.T) {
	steps := buildResults(
		[]SendResultRow{sendRow(1, uuid.Nil, 1000, 0, 0), sendRow(1, variantB, 1000, 0, 0)},
		[]OutcomeResultRow{replyRow(1, uuid.Nil, 10), replyRow(1, variantB, 40)},
		labels(), nil,
	)
	step := onlyStep(t, steps)
	if step.Winner != "B" {
		t.Errorf("winner = %q, want B (4x the reply rate on 1000 sends each)", step.Winner)
	}
}

func TestPickWinnerRefusesOnASmallSample(t *testing.T) {
	steps := buildResults(
		[]SendResultRow{sendRow(1, uuid.Nil, 50, 0, 0), sendRow(1, variantB, 50, 0, 0)},
		[]OutcomeResultRow{replyRow(1, variantB, 3)},
		labels(), nil,
	)
	step := onlyStep(t, steps)
	if step.Winner != "" {
		t.Errorf("winner = %q, want none on 50 sends", step.Winner)
	}
	if step.WinnerNote == "" {
		t.Error("an absent winner must be explained, never left blank")
	}
}

// The failure this rule exists to prevent: two arms a rounding error apart,
// with enough volume to look authoritative.
func TestPickWinnerRefusesWhenTheLeadIsWithinNoise(t *testing.T) {
	steps := buildResults(
		[]SendResultRow{sendRow(1, uuid.Nil, 1000, 0, 0), sendRow(1, variantB, 1000, 0, 0)},
		[]OutcomeResultRow{replyRow(1, uuid.Nil, 20), replyRow(1, variantB, 21)},
		labels(), nil,
	)
	step := onlyStep(t, steps)
	if step.Winner != "" {
		t.Errorf("winner = %q, want none for a 2.0%% vs 2.1%% split", step.Winner)
	}
	if step.WinnerNote == "" {
		t.Error("want an explanation for the missing winner")
	}
}

func TestPickWinnerRefusesWithNoRepliesAtAll(t *testing.T) {
	steps := buildResults(
		[]SendResultRow{sendRow(1, uuid.Nil, 1000, 500, 200), sendRow(1, variantB, 1000, 400, 100)},
		nil, labels(), nil,
	)
	step := onlyStep(t, steps)
	if step.Winner != "" {
		t.Errorf("winner = %q, want none — opens and clicks do not decide this", step.Winner)
	}
}

// A step with one arm is not an A/B test, so there is nothing to say — and no
// "no winner yet" note either, which would imply a comparison is pending.
func TestPickWinnerSaysNothingForASingleArm(t *testing.T) {
	steps := buildResults(
		[]SendResultRow{sendRow(1, uuid.Nil, 5000, 0, 0)},
		[]OutcomeResultRow{replyRow(1, uuid.Nil, 300)},
		labels(), nil,
	)
	step := onlyStep(t, steps)
	if step.Winner != "" || step.WinnerNote != "" {
		t.Errorf("winner = %q note = %q, want both empty for a single-arm step", step.Winner, step.WinnerNote)
	}
}

// Steps come back in send order regardless of map iteration order.
func TestBuildResultsOrdersStepsBySendOrder(t *testing.T) {
	steps := buildResults(
		[]SendResultRow{sendRow(3, uuid.Nil, 1, 0, 0), sendRow(1, uuid.Nil, 1, 0, 0), sendRow(2, uuid.Nil, 1, 0, 0)},
		nil, labels(), nil,
	)
	if len(steps) != 3 || steps[0].StepOrder != 1 || steps[1].StepOrder != 2 || steps[2].StepOrder != 3 {
		t.Fatalf("step order = %+v, want 1,2,3", steps)
	}
}
