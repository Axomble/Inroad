package campaign

import (
	"context"
	"sort"

	"github.com/google/uuid"
)

// ResultRow is one arm's performance: a step, one of its variants (or its base
// copy), and what that copy achieved.
type ResultRow struct {
	StepOrder int32
	// VariantID is uuid.Nil for the step's own base copy. That is also every send
	// made before variants existed, so a campaign that has never run an A/B test
	// reports as one unnamed arm per step rather than as missing data.
	VariantID uuid.UUID
	// Label is the variant's operator-facing name, or "A" for the base copy.
	Label   string
	IsBase  bool
	Weight  int32
	Sent    int64
	Opens   int64
	Clicks  int64
	Replies int64
	Bounces int64
	Unsubs  int64

	// Rates use TWO denominators, for the same reason the campaign rollup does.
	// OpenRate/ClickRate are per SEND (each message is tracked separately);
	// ReplyRate/BounceRate/UnsubRate are per send too HERE rather than per
	// enrollment, because an outcome is attributed to the last message received
	// (see queries/results.sql) and that message is a send of this arm. Dividing
	// by enrollments would mix a per-arm numerator with a per-campaign
	// denominator and let the rates exceed 1.
	OpenRate   float64
	ClickRate  float64
	ReplyRate  float64
	BounceRate float64
	UnsubRate  float64
}

// StepResults is one step's arms plus the step's own totals.
type StepResults struct {
	StepOrder int32
	Subject   string
	Rows      []ResultRow
	// Winner is the label of the arm with the highest reply rate, or "" when the
	// step has fewer than two arms, when nothing has sent, or when the leader is
	// not yet distinguishable (see pickWinner). It is a READING of the numbers,
	// deliberately conservative: an operator promoting an arm on 3 replies out of
	// 11 is the failure mode this feature would otherwise cause.
	Winner string
	// WinnerNote explains why there is no winner yet, so the empty string is
	// never left to be interpreted.
	WinnerNote string
}

// CampaignResults is GET /campaigns/{id}/results.
type CampaignResults struct {
	CampaignID uuid.UUID
	Steps      []StepResults
}

// minSampleForWinner is how many sends ONE arm needs before this will name it a
// winner. 200 is not a significance test -- it is a floor below which the
// question is not worth answering, chosen because a typical cold-email reply
// rate of 1-5% produces single-digit replies under it, where one extra reply
// swings the ranking. Calling a winner there is worse than saying nothing,
// because the operator acts on it.
const minSampleForWinner = 200

// minWinnerMargin is how much better the leader's reply rate must be, relative
// to the runner-up, to be called. 25% relative keeps a 2.0%-vs-1.9% split
// reading as "too close", which is what it is.
const minWinnerMargin = 1.25

// ResultsStore is the seam for the two aggregate reads. Separate from Store
// because it is a distinct, read-only responsibility, and keeping it narrow is
// what lets the winner logic be tested against fixed numbers.
type ResultsStore interface {
	// SendResults returns sends/opens/clicks keyed by (step_order, variant_id).
	SendResults(ctx context.Context, ws, campaignID uuid.UUID) ([]SendResultRow, error)
	// OutcomeResults returns stop-reason counts keyed by (step_order,
	// variant_id, reason).
	OutcomeResults(ctx context.Context, ws, campaignID uuid.UUID) ([]OutcomeResultRow, error)
	// VariantLabels maps a variant id to its label and weight, for the arms that
	// still exist. A send whose variant has since been deleted resolves to no
	// entry and is reported under its id, never silently folded into the base.
	VariantLabels(ctx context.Context, ws, campaignID uuid.UUID) (map[uuid.UUID]VariantLabel, error)
}

// SendResultRow is one (step, variant) engagement aggregate.
type SendResultRow struct {
	StepOrder int32
	VariantID uuid.UUID
	Sent      int64
	Opens     int64
	Clicks    int64
}

// OutcomeResultRow is one (step, variant, reason) outcome aggregate.
type OutcomeResultRow struct {
	StepOrder  int32
	VariantID  uuid.UUID
	StopReason string
	Count      int64
}

// VariantLabel is a variant's operator-facing identity.
type VariantLabel struct {
	Label  string
	Weight int32
}

// Results loads and assembles the per-step, per-variant report.
func (s *Service) Results(ctx context.Context, ws, campaignID uuid.UUID) (CampaignResults, error) {
	if _, err := s.store.Get(ctx, ws, campaignID); err != nil {
		return CampaignResults{}, ErrNotFound
	}
	if s.results == nil {
		return CampaignResults{}, ErrResultsUnavailable
	}
	sends, err := s.results.SendResults(ctx, ws, campaignID)
	if err != nil {
		return CampaignResults{}, err
	}
	outcomes, err := s.results.OutcomeResults(ctx, ws, campaignID)
	if err != nil {
		return CampaignResults{}, err
	}
	labels, err := s.results.VariantLabels(ctx, ws, campaignID)
	if err != nil {
		return CampaignResults{}, err
	}
	steps, err := s.store.ListSteps(ctx, ws, campaignID)
	if err != nil {
		return CampaignResults{}, err
	}
	subjects := make(map[int32]string, len(steps))
	for _, st := range steps {
		subjects[st.StepOrder] = st.Subject
	}
	return CampaignResults{
		CampaignID: campaignID,
		Steps:      buildResults(sends, outcomes, labels, subjects),
	}, nil
}

// buildResults is the pure assembly: no I/O, so the rate arithmetic and the
// winner rule are unit-tested against fixed numbers.
func buildResults(
	sends []SendResultRow,
	outcomes []OutcomeResultRow,
	labels map[uuid.UUID]VariantLabel,
	subjects map[int32]string,
) []StepResults {
	type key struct {
		step    int32
		variant uuid.UUID
	}
	rows := make(map[key]*ResultRow, len(sends))
	for _, s := range sends {
		k := key{s.StepOrder, s.VariantID}
		rows[k] = &ResultRow{
			StepOrder: s.StepOrder, VariantID: s.VariantID,
			Sent: s.Sent, Opens: s.Opens, Clicks: s.Clicks,
		}
	}
	for _, o := range outcomes {
		k := key{o.StepOrder, o.VariantID}
		row, ok := rows[k]
		if !ok {
			// An outcome with no matching send aggregate cannot normally happen
			// (both derive from the same sent rows), but a row is created rather
			// than dropped so a count can never silently vanish from the report.
			row = &ResultRow{StepOrder: o.StepOrder, VariantID: o.VariantID}
			rows[k] = row
		}
		switch o.StopReason {
		case stopReasonReplied:
			row.Replies += o.Count
		case stopReasonBounced:
			row.Bounces += o.Count
		case stopReasonSuppressed:
			row.Unsubs += o.Count
		}
	}

	byStep := map[int32][]ResultRow{}
	for k, row := range rows {
		row.IsBase = row.VariantID == uuid.Nil
		if row.IsBase {
			row.Label = "A"
		} else if label, ok := labels[row.VariantID]; ok {
			row.Label, row.Weight = label.Label, label.Weight
		} else {
			// The variant row is gone (deleted while it had no sends, then sends
			// arrived from an in-flight job). Naming it by id beats folding its
			// numbers into another arm's.
			row.Label = "deleted (" + row.VariantID.String()[:8] + ")"
		}
		applyRates(row)
		byStep[k.step] = append(byStep[k.step], *row)
	}

	out := make([]StepResults, 0, len(byStep))
	for step, stepRows := range byStep {
		sort.Slice(stepRows, func(i, j int) bool {
			if stepRows[i].IsBase != stepRows[j].IsBase {
				return stepRows[i].IsBase // base copy first — it is variant A
			}
			return stepRows[i].Label < stepRows[j].Label
		})
		winner, note := pickWinner(stepRows)
		out = append(out, StepResults{
			StepOrder: step, Subject: subjects[step], Rows: stepRows,
			Winner: winner, WinnerNote: note,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StepOrder < out[j].StepOrder })
	return out
}

// applyRates divides each numerator by sends, guarding zero rather than
// producing NaN — a NaN would serialise as null and render as a blank cell that
// reads like missing data instead of "nothing sent yet".
func applyRates(r *ResultRow) {
	if r.Sent == 0 {
		return
	}
	sent := float64(r.Sent)
	r.OpenRate = float64(r.Opens) / sent
	r.ClickRate = float64(r.Clicks) / sent
	r.ReplyRate = float64(r.Replies) / sent
	r.BounceRate = float64(r.Bounces) / sent
	r.UnsubRate = float64(r.Unsubs) / sent
}

// pickWinner names the best arm by reply rate, or explains why it will not.
//
// Reply rate is the criterion because it is the only one of the five that
// measures the thing a cold-email campaign is for. Opens are proxy-inflated and
// unmeasurable with tracking off; clicks depend on whether the copy has a link
// at all, which differs BETWEEN arms and would rank a variant for containing a
// link rather than for working.
//
// It refuses to answer more often than it answers, deliberately. Naming a
// winner is an instruction to promote one and retire the other, and the cost of
// doing that on noise is a worse campaign plus a false belief about why.
func pickWinner(rows []ResultRow) (string, string) {
	if len(rows) < 2 {
		return "", "" // nothing to compare — not a "no winner yet" state
	}
	ranked := make([]ResultRow, len(rows))
	copy(ranked, rows)
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].ReplyRate > ranked[j].ReplyRate })

	leader, runnerUp := ranked[0], ranked[1]
	if leader.Sent < minSampleForWinner {
		return "", "Not enough sends yet to compare — keep going."
	}
	if leader.Replies == 0 {
		return "", "No replies on any variant yet."
	}
	if runnerUp.ReplyRate > 0 && leader.ReplyRate < runnerUp.ReplyRate*minWinnerMargin {
		return "", "Too close to call — the leading variant isn’t clearly ahead."
	}
	return leader.Label, ""
}
