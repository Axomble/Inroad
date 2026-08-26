package sandbox

import "time"

// Timeline is the simulated history of one persona's journey through one
// campaign: the steps that went out, and the engagement each provoked. It is
// computed as pure data before anything is written, so the shape of a run can
// be asserted in a unit test without a database.
type Timeline struct {
	Persona Persona
	Sends   []SendEvent
	// Reply is the persona's reply, if they sent one. At most one: a prospect
	// who answers stops the sequence, which is exactly what the real
	// reply-handling path does (sequence_enrollments.stop_reason = 'replied').
	Reply *ReplyEvent
	// StopReason mirrors sequence_enrollments.stop_reason for this journey:
	// "replied", "bounced", or "" when the sequence ran its course.
	StopReason string
}

// SendEvent is one outbound step that actually went out, with the engagement
// it drew. A send is always 'sent' here — a queued or failed send never
// happened and must not appear in a thread (see the sent_at IS NOT NULL guard
// on ListSentOutboundStepsForThread).
type SendEvent struct {
	StepOrder int32
	SentAt    time.Time
	// OpenedAt/ClickedAt are nil when the persona did not do that thing. Only
	// ever set on a send whose persona opens/clicks, and always strictly after
	// SentAt — a tracking event before its own send would be nonsense in the
	// reporting screens.
	OpenedAt  *time.Time
	ClickedAt *time.Time
	// BouncedAt is set only on the first step of a bouncing persona: the
	// bounce stops the sequence, so no later step exists to bounce.
	BouncedAt *time.Time
}

// ReplyEvent is the inbound message a persona sent back, and the step it was
// replying to. InReplyToStep names the send whose Message-ID the reply
// threads against — the root the inbox anchors the whole conversation on.
type ReplyEvent struct {
	At              time.Time
	InReplyToStep   int32
	Flavor          ReplyFlavor
	Subject         string
	BodyText        string
	SecondsAfterRun time.Duration
}

// CampaignShape describes the sequence a Timeline is generated against: how
// many steps it has and how long the gap between them is. Kept separate from
// the step content so the timeline logic never has to know about copy.
type CampaignShape struct {
	Steps []StepShape
}

// StepShape is one step's cadence: the delay after the previous step before
// this one goes out.
type StepShape struct {
	Order        int32
	DelaySeconds int32
}

// BuildTimeline computes one persona's journey. now is the instant the run is
// anchored to (the "end" of the simulated history) and window is how far back
// the campaign's first sends reach. Every timestamp produced is in the past
// relative to now, because the harness seeds history, not a schedule.
//
// The staggering is the point: enrolments start on different days across the
// window and steps then follow their own cadence, so campaign reporting shows
// a curve rather than one spike, and the inbox's "today"/"this week" scopes
// have different contents.
func BuildTimeline(p Persona, index int, shape CampaignShape, now time.Time, window time.Duration) Timeline {
	tl := Timeline{Persona: p}
	if len(shape.Steps) == 0 {
		return tl
	}

	start := enrollmentStart(index, now, window)
	sentAt := start

	for i, step := range shape.Steps {
		if i > 0 {
			sentAt = sentAt.Add(time.Duration(step.DelaySeconds) * time.Second)
		}
		// A step scheduled past "now" simply has not happened yet: the
		// sequence is still in flight for this contact, which is a legitimate
		// and necessary state for the campaign screens to show.
		if sentAt.After(now) {
			break
		}

		ev := SendEvent{StepOrder: step.Order, SentAt: sentAt}

		if p.Behavior.Bounces {
			// A bounce lands within minutes of the send and stops everything.
			bounced := sentAt.Add(bounceDelay(index))
			if bounced.After(now) {
				bounced = now
			}
			ev.BouncedAt = &bounced
			tl.Sends = append(tl.Sends, ev)
			tl.StopReason = "bounced"
			return tl
		}

		if p.Behavior.Opens {
			if opened := sentAt.Add(openDelay(index, i)); !opened.After(now) {
				ev.OpenedAt = &opened
				// A click always follows its own open, never precedes it.
				if p.Behavior.Clicks {
					if clicked := opened.Add(clickDelay(index, i)); !clicked.After(now) {
						ev.ClickedAt = &clicked
					}
				}
			}
		}
		tl.Sends = append(tl.Sends, ev)

		// The reply lands against the step the persona had most recently
		// received, and ends the sequence there.
		if p.Behavior.Replies && ev.OpenedAt != nil {
			at := ev.OpenedAt.Add(replyDelay(index, i))
			if at.After(now) {
				break
			}
			body, subject := replyContent(p, p.Behavior.Reply)
			tl.Reply = &ReplyEvent{
				At: at, InReplyToStep: step.Order, Flavor: p.Behavior.Reply,
				Subject: subject, BodyText: body,
			}
			tl.StopReason = "replied"
			return tl
		}
	}
	return tl
}

// Delay bounds for the simulated engagement. Real prospects read mail on a
// human clock: an open minutes-to-a-day after the send, a click shortly after
// the open, a reply anywhere from under an hour to a couple of days later.
const (
	minOpenDelay  = 3 * time.Minute
	maxOpenDelay  = 30 * time.Hour
	minClickDelay = 20 * time.Second
	maxClickDelay = 45 * time.Minute
	minReplyDelay = 12 * time.Minute
	maxReplyDelay = 40 * time.Hour
	minBounce     = 5 * time.Second
	maxBounce     = 4 * time.Minute
)

// enrollmentStart spreads first sends across the window. Contacts are not
// enrolled in one batch at one instant: a campaign that shows every send on a
// single timestamp makes the daily charts a single bar and the inbox's
// time-window scopes indistinguishable.
//
// The offset also carries a within-day component, so sends land at varied
// hours rather than all at the same clock time each day.
func enrollmentStart(index int, now time.Time, window time.Duration) time.Time {
	if window <= 0 {
		return now
	}
	// Two independent draws: which day into the window, and where in that day.
	days := int(window / (24 * time.Hour))
	if days < 1 {
		days = 1
	}
	dayOffset := percentile(index, "enroll-day") * days / 100
	withinDay := time.Duration(percentile(index, "enroll-hour")) * window / 100 % (24 * time.Hour)
	return now.Add(-window).
		Add(time.Duration(dayOffset) * 24 * time.Hour).
		Add(withinDay)
}

// spread maps a 0..99 percentile onto [min, max]. One helper for every delay
// so the delays share a single, testable definition of "somewhere in range".
func spread(p int, lo, hi time.Duration) time.Duration {
	return lo + time.Duration(int64(hi-lo)*int64(p)/100)
}

func openDelay(index, step int) time.Duration {
	return spread(percentile(index*31+step, "open-delay"), minOpenDelay, maxOpenDelay)
}

func clickDelay(index, step int) time.Duration {
	return spread(percentile(index*37+step, "click-delay"), minClickDelay, maxClickDelay)
}

func replyDelay(index, step int) time.Duration {
	return spread(percentile(index*41+step, "reply-delay"), minReplyDelay, maxReplyDelay)
}

func bounceDelay(index int) time.Duration {
	return spread(percentile(index, "bounce-delay"), minBounce, maxBounce)
}
