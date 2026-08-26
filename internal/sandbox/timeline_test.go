package sandbox

import (
	"strings"
	"testing"
	"time"
)

// runTimelines builds the whole simulated population once, the way the seeder
// does, so the assertions below read the same data the writer would.
func runTimelines(t *testing.T, n int, window time.Duration) (time.Time, []Timeline) {
	t.Helper()
	now := time.Date(2026, 8, 26, 15, 0, 0, 0, time.UTC)
	shape := Shape(campaignSteps)
	out := make([]Timeline, 0, n)
	for i, p := range BuildPersonas(n) {
		out = append(out, BuildTimeline(p, i, shape, now, window))
	}
	return now, out
}

func TestBuildTimelineWithNoStepsProducesNothing(t *testing.T) {
	now := time.Now()
	tl := BuildTimeline(personaAt(0), 0, CampaignShape{}, now, 14*24*time.Hour)
	if len(tl.Sends) != 0 || tl.Reply != nil {
		t.Errorf("empty campaign produced activity: %+v", tl)
	}
}

// The harness seeds HISTORY. A timestamp in the future would show up as a
// send that has not happened yet, and would break the daily rollups.
func TestNoEventIsInTheFuture(t *testing.T) {
	now, timelines := runTimelines(t, 150, 21*24*time.Hour)
	for i, tl := range timelines {
		for _, s := range tl.Sends {
			if s.SentAt.After(now) {
				t.Fatalf("timeline %d step %d sent in the future", i, s.StepOrder)
			}
			for name, at := range map[string]*time.Time{"open": s.OpenedAt, "click": s.ClickedAt, "bounce": s.BouncedAt} {
				if at != nil && at.After(now) {
					t.Fatalf("timeline %d step %d %s is in the future", i, s.StepOrder, name)
				}
			}
		}
		if tl.Reply != nil && tl.Reply.At.After(now) {
			t.Fatalf("timeline %d replied in the future", i)
		}
	}
}

// Engagement cannot precede the send that caused it, and a click cannot
// precede its own open — the reporting screens would show impossible ratios.
func TestEngagementFollowsItsSend(t *testing.T) {
	_, timelines := runTimelines(t, 300, 21*24*time.Hour)
	for i, tl := range timelines {
		for _, s := range tl.Sends {
			if s.OpenedAt != nil && !s.OpenedAt.After(s.SentAt) {
				t.Errorf("timeline %d: open %v not after send %v", i, s.OpenedAt, s.SentAt)
			}
			if s.ClickedAt != nil {
				if s.OpenedAt == nil {
					t.Errorf("timeline %d: click with no open", i)
				} else if !s.ClickedAt.After(*s.OpenedAt) {
					t.Errorf("timeline %d: click %v not after open %v", i, s.ClickedAt, s.OpenedAt)
				}
			}
			if s.BouncedAt != nil && !s.BouncedAt.After(s.SentAt) {
				t.Errorf("timeline %d: bounce %v not after send %v", i, s.BouncedAt, s.SentAt)
			}
		}
		if tl.Reply != nil {
			last := tl.Sends[len(tl.Sends)-1]
			if !tl.Reply.At.After(last.SentAt) {
				t.Errorf("timeline %d: reply %v not after the step it answers %v", i, tl.Reply.At, last.SentAt)
			}
		}
	}
}

// Steps must go out in order and on the campaign's cadence, or the thread
// reader renders the conversation out of sequence.
func TestStepsAreOrderedAndSpaced(t *testing.T) {
	_, timelines := runTimelines(t, 200, 30*24*time.Hour)
	for i, tl := range timelines {
		for j := 1; j < len(tl.Sends); j++ {
			prev, cur := tl.Sends[j-1], tl.Sends[j]
			if cur.StepOrder <= prev.StepOrder {
				t.Fatalf("timeline %d: step order went %d -> %d", i, prev.StepOrder, cur.StepOrder)
			}
			wantGap := time.Duration(campaignSteps[j].DelaySeconds) * time.Second
			if got := cur.SentAt.Sub(prev.SentAt); got != wantGap {
				t.Errorf("timeline %d: gap between step %d and %d = %v, want %v", i, prev.StepOrder, cur.StepOrder, got, wantGap)
			}
		}
	}
}

// A reply or a bounce stops the sequence; a later step after either would
// contradict what the real enrollment path does.
func TestReplyAndBounceStopTheSequence(t *testing.T) {
	_, timelines := runTimelines(t, 400, 30*24*time.Hour)
	var replied, bounced int
	for i, tl := range timelines {
		switch tl.StopReason {
		case "replied":
			replied++
			if tl.Reply == nil {
				t.Fatalf("timeline %d stopped for reply but has no reply", i)
			}
			if tl.Reply.InReplyToStep != tl.Sends[len(tl.Sends)-1].StepOrder {
				t.Errorf("timeline %d: reply answers step %d, not the last step sent", i, tl.Reply.InReplyToStep)
			}
		case "bounced":
			bounced++
			if len(tl.Sends) != 1 {
				t.Errorf("timeline %d bounced but sent %d steps", i, len(tl.Sends))
			}
			if tl.Sends[0].BouncedAt == nil {
				t.Errorf("timeline %d stopped for bounce but has no bounce timestamp", i)
			}
			if tl.Reply != nil {
				t.Errorf("timeline %d bounced and also replied", i)
			}
		case "":
			if tl.Reply != nil {
				t.Errorf("timeline %d has a reply but no stop reason", i)
			}
		default:
			t.Errorf("timeline %d has unknown stop reason %q", i, tl.StopReason)
		}
	}
	if replied == 0 || bounced == 0 {
		t.Errorf("population produced replied=%d bounced=%d, want both non-zero", replied, bounced)
	}
}

// The whole reason the harness staggers enrolment: activity has to land on
// many different days, or the campaign charts are one bar and the inbox's
// today/this-week scopes are indistinguishable.
func TestSendsAreSpreadAcrossTheWindow(t *testing.T) {
	const window = 21 * 24 * time.Hour
	_, timelines := runTimelines(t, 150, window)

	days := map[string]int{}
	hours := map[int]int{}
	for _, tl := range timelines {
		for _, s := range tl.Sends {
			days[s.SentAt.Format("2006-01-02")]++
			hours[s.SentAt.Hour()]++
		}
	}
	// A 21-day window with 150 contacts should touch most days in it.
	if len(days) < 14 {
		t.Errorf("sends landed on only %d distinct days, want >= 14", len(days))
	}
	// And not all at the same time of day.
	if len(hours) < 8 {
		t.Errorf("sends landed in only %d distinct hours, want >= 8", len(hours))
	}
	// No single day should dominate the whole run.
	for day, n := range days {
		if n > len(timelines) {
			t.Errorf("day %s holds %d sends, more than the %d contacts", day, n, len(timelines))
		}
	}
}

// Threads and campaign reporting both key off these, so a run that produced
// no replies would seed an empty inbox and defeat the harness's purpose.
func TestPopulationProducesInboxWorthyVolume(t *testing.T) {
	_, timelines := runTimelines(t, 150, 21*24*time.Hour)
	var sends, opens, clicks, replies int
	for _, tl := range timelines {
		sends += len(tl.Sends)
		for _, s := range tl.Sends {
			if s.OpenedAt != nil {
				opens++
			}
			if s.ClickedAt != nil {
				clicks++
			}
		}
		if tl.Reply != nil {
			replies++
		}
	}
	if sends < 150 {
		t.Errorf("only %d sends for 150 contacts", sends)
	}
	if opens == 0 || clicks == 0 || replies == 0 {
		t.Errorf("thin run: opens=%d clicks=%d replies=%d", opens, clicks, replies)
	}
}

// Steps 2+ carry an empty subject on purpose (the "reply in the same thread"
// convention); a non-empty one would seed threads the send path could not
// have produced.
func TestFollowUpStepsThreadOnStepOne(t *testing.T) {
	if campaignSteps[0].Subject == "" {
		t.Fatal("step 1 must carry a subject: it is the thread's anchor")
	}
	for _, s := range campaignSteps[1:] {
		if s.Subject != "" {
			t.Errorf("step %d subject = %q, want empty so it renders as Re: step 1", s.Order, s.Subject)
		}
	}
}

func TestReplyContentIsAddressedToTheThread(t *testing.T) {
	p := personaAt(3)
	for _, f := range []ReplyFlavor{ReplyPositive, ReplyQuestion, ReplyNegative, ReplyOutOfOffice, ReplyUnsubscribe} {
		body, subject := replyContent(p, f)
		if body == "" {
			t.Errorf("flavor %d produced an empty body", f)
		}
		if !strings.HasPrefix(subject, "Re: ") {
			t.Errorf("flavor %d subject %q does not start with %q", f, subject, "Re: ")
		}
		if !strings.Contains(subject, p.Company) {
			t.Errorf("flavor %d subject %q does not carry the rendered company %q", f, subject, p.Company)
		}
		if strings.Contains(subject, "{{") {
			t.Errorf("flavor %d subject %q has an unrendered placeholder", f, subject)
		}
	}
}
