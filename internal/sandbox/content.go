package sandbox

import (
	"fmt"
	"strings"
)

// The campaign the harness seeds. Two follow-ups after the opener, on a
// three-then-four-day cadence — a shape a real outbound sequence actually
// uses, and enough steps that a thread shows more than one outbound message.
//
// Steps 2 and 3 deliberately carry an EMPTY subject. That is the product's
// "reply in the same thread" convention (see inbox.replySubject and the send
// path's identical rule): an empty step subject renders as "Re: <step 1
// subject>". Giving them their own subjects would seed threads that the real
// send path would never have produced.
var campaignSteps = []StepContent{
	{
		Order: 1, DelaySeconds: 0,
		Subject: "{{company}} + fewer manual handoffs",
		BodyText: "Hi {{first_name}},\n\n" +
			"I work with teams at companies like {{company}} who are stitching outbound together by hand — " +
			"a spreadsheet of prospects, a mailbox that keeps hitting limits, and no idea which message actually landed.\n\n" +
			"We fixed that end to end. Worth fifteen minutes to see whether it maps onto how your team runs?\n\n" +
			"Best,\n{{sender_name}}",
	},
	{
		Order: 2, DelaySeconds: 3 * 24 * 3600,
		BodyText: "Hi {{first_name}},\n\n" +
			"Following up on the note below — happy to send the two-minute version instead of a call if that is easier.\n\n" +
			"Best,\n{{sender_name}}",
	},
	{
		Order: 3, DelaySeconds: 4 * 24 * 3600,
		BodyText: "Hi {{first_name}},\n\n" +
			"Last note from me so I am not cluttering your inbox. If outbound tooling comes back up as a priority at {{company}}, " +
			"I am easy to find.\n\n" +
			"Best,\n{{sender_name}}",
	},
}

// StepContent is one seeded sequence step: its cadence plus its copy. The
// bodies carry the product's real {{first_name}}/{{company}} placeholders, so
// the seeded campaign exercises template rendering rather than sidestepping it.
type StepContent struct {
	Order        int32
	DelaySeconds int32
	Subject      string
	BodyText     string
}

// Shape reduces the seeded step content to the cadence-only view the timeline
// generator needs, so timeline logic never depends on copy.
func Shape(steps []StepContent) CampaignShape {
	out := CampaignShape{Steps: make([]StepShape, 0, len(steps))}
	for _, s := range steps {
		out.Steps = append(out.Steps, StepShape{Order: s.Order, DelaySeconds: s.DelaySeconds})
	}
	return out
}

// BodyHTML renders a step's plain-text body as minimal paragraph HTML. The
// send path stores both, and the inbox thread reader prefers the HTML leg, so
// seeding text alone would render the outbound messages blank.
func (s StepContent) BodyHTML() string { return textToHTML(s.BodyText) }

// replyContent writes the inbound reply for a flavour: the body a prospect
// would plausibly send, and the subject line it arrives under.
//
// The subject is always "Re: " + the campaign's step-1 subject with the
// contact's company substituted, because that is what a real mail client
// sends back and what the thread's own subject has to agree with.
func replyContent(p Persona, f ReplyFlavor) (body, subject string) {
	subject = "Re: " + renderTemplate(campaignSteps[0].Subject, p, "")
	switch f {
	case ReplyPositive:
		return fmt.Sprintf(
			"Hi,\n\nGood timing — this is on our list for next quarter and the manual handoffs are exactly the problem.\n\n"+
				"Can you send pricing and a couple of references in our space? Happy to find thirty minutes after that.\n\nThanks,\n%s",
			p.FirstName), subject
	case ReplyQuestion:
		return fmt.Sprintf(
			"Hi,\n\nBefore I take this to the team: does it handle multiple sending domains, and where is the data hosted?\n\n"+
				"We are in a regulated space so the second one decides it for us.\n\n%s",
			p.FirstName), subject
	case ReplyNegative:
		return fmt.Sprintf(
			"Hi,\n\nWe just signed with another vendor for this, so not a fit right now.\n\nWorth trying again next year.\n\n%s",
			p.FirstName), subject
	case ReplyOutOfOffice:
		return fmt.Sprintf(
			"Hello,\n\nI am out of the office until the end of next week with limited access to email.\n\n"+
				"For anything urgent please contact our team at hello@%s.\n\nThis is an automated reply.",
			p.Domain), subject
	case ReplyUnsubscribe:
		return fmt.Sprintf(
			"Please remove me from this list and do not contact me again.\n\n%s",
			p.FirstName), subject
	default:
		return fmt.Sprintf("Hi,\n\nThanks for reaching out.\n\n%s", p.FirstName), subject
	}
}

// renderTemplate substitutes the campaign placeholders the seeded copy uses.
// This mirrors what the send path does at delivery time; the harness renders
// them itself because it writes the resulting history directly rather than
// going through the sender.
func renderTemplate(s string, p Persona, senderName string) string {
	return strings.NewReplacer(
		"{{first_name}}", p.FirstName,
		"{{last_name}}", p.LastName,
		"{{company}}", p.Company,
		"{{sender_name}}", senderName,
	).Replace(s)
}
