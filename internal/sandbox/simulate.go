package sandbox

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Target names the already-created workspace rows a run writes against. The
// harness simulates ACTIVITY; it does not own workspace creation, which is
// cmd/seed's job (and goes through identity.Service.Register so the workspace
// gets its DEK, its reply labels and everything else a real signup gets).
type Target struct {
	WorkspaceID uuid.UUID
	CampaignID  uuid.UUID
	MailboxID   uuid.UUID
	// MailboxEmail/MailboxName are the sending identity. They are written onto
	// every reply's to_email so the seeded inbound mail is addressed to the
	// mailbox that sent the campaign, as real replies are.
	MailboxEmail string
	MailboxName  string
	// Contacts pairs each persona with the contact row already created for it.
	Contacts []TargetContact
}

// TargetContact links a generated persona to its persisted contact id.
type TargetContact struct {
	ContactID uuid.UUID
	Persona   Persona
}

// Options tune one simulated run.
type Options struct {
	// Window is how far back the simulated history reaches. Sends are spread
	// across it rather than clustered, so the daily charts have a curve.
	Window time.Duration
	// Now anchors the run. Zero means time.Now; injected so a test can assert
	// against a fixed instant.
	Now time.Time
	// MessageIDDomain is the domain simulated Message-IDs are minted under.
	MessageIDDomain string
	// Deliverer optionally sends each simulated message through a real SMTP
	// hop (Mailpit) as well as recording it. nil records only.
	Deliverer Deliverer
}

// Defaults for an unset Options field.
const (
	DefaultWindow          = 21 * 24 * time.Hour
	DefaultMessageIDDomain = "sandbox.inroad.test"
)

// Result reports what a run wrote, so the caller can print a summary that
// tells an operator whether the workspace is worth looking at.
type Result struct {
	Contacts  int
	Sends     int
	Opens     int
	Clicks    int
	Replies   int
	Bounces   int
	Threads   int
	Delivered int
}

// String renders the result as the one-line summary cmd/seed prints.
func (r Result) String() string {
	return fmt.Sprintf(
		"%d contacts, %d sends, %d opens, %d clicks, %d replies (%d threads), %d bounces, %d delivered to SMTP",
		r.Contacts, r.Sends, r.Opens, r.Clicks, r.Replies, r.Threads, r.Bounces, r.Delivered)
}

// Simulator replays generated timelines into the database. It depends on the
// Store interface, never on a concrete pool, so its behaviour is unit-tested
// against a fake.
type Simulator struct {
	store  Store
	enroll EnrollmentWriter
	opts   Options
}

// NewSimulator builds a Simulator, filling in Options defaults.
func NewSimulator(store Store, enroll EnrollmentWriter, opts Options) *Simulator {
	if opts.Window <= 0 {
		opts.Window = DefaultWindow
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	if opts.MessageIDDomain == "" {
		opts.MessageIDDomain = DefaultMessageIDDomain
	}
	return &Simulator{store: store, enroll: enroll, opts: opts}
}

// Run simulates the whole campaign against every contact in the target.
//
// A failure on any single contact aborts the run rather than being logged and
// skipped: a half-seeded workspace that reports success is worse than a
// failure an operator can re-run, because the missing rows only show up later
// as an inexplicably empty screen.
func (s *Simulator) Run(ctx context.Context, target Target) (Result, error) {
	res := Result{Contacts: len(target.Contacts)}
	shape := Shape(campaignSteps)

	for i, tc := range target.Contacts {
		tl := BuildTimeline(tc.Persona, i, shape, s.opts.Now, s.opts.Window)
		if err := s.runOne(ctx, target, tc, tl, &res); err != nil {
			return res, fmt.Errorf("simulate %s: %w", tc.Persona.Email, err)
		}
	}
	return res, nil
}

// runOne replays one contact's timeline: the enrollment, each send with its
// tracking, then the reply or bounce outcome.
func (s *Simulator) runOne(ctx context.Context, target Target, tc TargetContact, tl Timeline, res *Result) error {
	if len(tl.Sends) == 0 {
		return nil
	}

	// The root Message-ID is step 1's, and every later step threads against
	// it. This single value is what ties the whole conversation together: the
	// enrollment's thread_root_id, each follow-up's References header, and —
	// critically — the inbox thread's root_message_id all carry it.
	root := s.messageID(target, tc, 1)
	last := tl.Sends[len(tl.Sends)-1]

	if err := s.enroll.EnsureEnrollment(ctx, EnrollmentRecord{
		WorkspaceID: target.WorkspaceID, CampaignID: target.CampaignID, ContactID: tc.ContactID,
		CurrentStep: last.StepOrder, EnrolledAt: tl.Sends[0].SentAt,
		LastSentAt: last.SentAt, ThreadRootID: root,
	}); err != nil {
		return err
	}

	for _, ev := range tl.Sends {
		if err := s.replaySend(ctx, target, tc, ev, root, res); err != nil {
			return err
		}
	}

	return s.replayOutcome(ctx, target, tc, tl, root, res)
}

// replaySend writes one completed step and the tracking it drew.
func (s *Simulator) replaySend(ctx context.Context, target Target, tc TargetContact, ev SendEvent, root string, res *Result) error {
	// References is empty on step 1 (it IS the root) and the root thereafter,
	// exactly as the send path builds the chain.
	references := ""
	if ev.StepOrder > 1 {
		references = root
	}
	sendID, err := s.store.RecordSend(ctx, SendRecord{
		WorkspaceID: target.WorkspaceID, CampaignID: target.CampaignID, ContactID: tc.ContactID,
		MailboxID: target.MailboxID, ToEmail: tc.Persona.Email, StepOrder: ev.StepOrder,
		MessageID: s.messageID(target, tc, ev.StepOrder), ReferencesHeader: references, SentAt: ev.SentAt,
	})
	if err != nil {
		return err
	}
	res.Sends++

	if ev.OpenedAt != nil {
		if err := s.store.RecordTracking(ctx, TrackingRecord{
			WorkspaceID: target.WorkspaceID, CampaignID: target.CampaignID, SendID: sendID,
			Kind: "open", UserAgent: userAgentFor(tc.Persona), At: *ev.OpenedAt,
		}); err != nil {
			return err
		}
		res.Opens++
	}
	if ev.ClickedAt != nil {
		if err := s.store.RecordTracking(ctx, TrackingRecord{
			WorkspaceID: target.WorkspaceID, CampaignID: target.CampaignID, SendID: sendID,
			Kind: "click", URL: "https://inroad.test/product-overview",
			UserAgent: userAgentFor(tc.Persona), At: *ev.ClickedAt,
		}); err != nil {
			return err
		}
		res.Clicks++
	}

	if s.opts.Deliverer != nil {
		step := campaignSteps[ev.StepOrder-1]
		if err := s.opts.Deliverer.Deliver(ctx, OutboundMessage{
			FromEmail: target.MailboxEmail, FromName: target.MailboxName,
			ToEmail: tc.Persona.Email, ToName: tc.Persona.FullName(),
			Subject:   s.subjectFor(step, tc.Persona, target.MailboxName),
			BodyText:  renderTemplate(step.BodyText, tc.Persona, target.MailboxName),
			MessageID: s.messageID(target, tc, ev.StepOrder), References: references, Date: ev.SentAt,
		}); err != nil {
			return fmt.Errorf("deliver step %d: %w", ev.StepOrder, err)
		}
		res.Delivered++
	}
	return nil
}

// subjectFor resolves a step's effective subject the same way the send path
// and the inbox reader both do: step 1 uses its own, a later step with an
// empty subject renders as "Re: <step 1 subject>".
func (s *Simulator) subjectFor(step StepContent, p Persona, senderName string) string {
	if step.Order <= 1 || step.Subject != "" {
		return renderTemplate(step.Subject, p, senderName)
	}
	return "Re: " + renderTemplate(campaignSteps[0].Subject, p, senderName)
}

// replayOutcome writes the terminal event: the inbound reply that lands in the
// inbox, or the bounce that suppresses the address. Both stop the enrollment,
// as the real handlers do.
func (s *Simulator) replayOutcome(ctx context.Context, target Target, tc TargetContact, tl Timeline, root string, res *Result) error {
	switch tl.StopReason {
	case "replied":
		if err := s.recordReply(ctx, target, tc, tl, root); err != nil {
			return err
		}
		res.Replies++
		res.Threads++
		if err := s.store.StopEnrollment(ctx, target.WorkspaceID, target.CampaignID, tc.ContactID, "replied", tl.Reply.At); err != nil {
			return err
		}
		// An unsubscribe reply suppresses the contact, matching the
		// suppresses_contact flag the builtin 'unsubscribe' label carries.
		if tl.Reply.Flavor == ReplyUnsubscribe {
			return s.store.SuppressContact(ctx, target.WorkspaceID, tc.Persona.Email, "unsubscribe")
		}
		return nil

	case "bounced":
		res.Bounces++
		bouncedAt := tl.Sends[0].BouncedAt
		if err := s.store.StopEnrollment(ctx, target.WorkspaceID, target.CampaignID, tc.ContactID, "bounced", *bouncedAt); err != nil {
			return err
		}
		return s.store.SuppressContact(ctx, target.WorkspaceID, tc.Persona.Email, "bounce")

	default:
		return nil
	}
}

// recordReply writes the inbound leg of the conversation.
//
// Only the INBOUND message is written. The outbound leg is deliberately NOT
// inserted into inbox_messages: the inbox synthesizes it at read time by
// joining sends to sequence_steps on (campaign_id, step_order), so the sends
// rows written above already ARE the outbound half of this thread. Writing it
// again here would render every outbound message twice.
//
// That is also why campaign_id and contact_id must be set on the thread: they
// are the only keys the synthesizing join has. A thread missing either shows
// the reply with no conversation above it.
func (s *Simulator) recordReply(ctx context.Context, target Target, tc TargetContact, tl Timeline, root string) error {
	r := tl.Reply
	return s.store.RecordInboundReply(ctx, ReplyRecord{
		WorkspaceID: target.WorkspaceID,
		MailboxID:   target.MailboxID,
		CampaignID:  target.CampaignID,
		ContactID:   tc.ContactID,
		// Anchored on step 1's Message-ID, NOT on the step being replied to:
		// this is what groups every message onto one thread.
		RootMessageID: root,
		Subject:       r.Subject,
		ReplyClass:    r.Flavor.LabelKey(),
		MessageID:     s.replyMessageID(target, tc),
		FromEmail:     tc.Persona.Email,
		FromName:      tc.Persona.FullName(),
		ToEmail:       target.MailboxEmail,
		BodyText:      r.BodyText,
		BodyHTML:      textToHTML(r.BodyText),
		OccurredAt:    r.At,
	})
}

// messageID mints the deterministic Message-ID for one (contact, step). It is
// derived rather than random so a re-run produces the same identifiers, which
// is what makes the whole harness idempotent: the send upsert, the thread's
// root and the message dedupe all key off these.
func (s *Simulator) messageID(target Target, tc TargetContact, step int32) string {
	return fmt.Sprintf("<sbx-%s-%s-s%d@%s>",
		short(target.CampaignID), short(tc.ContactID), step, s.opts.MessageIDDomain)
}

// replyMessageID mints the inbound reply's own Message-ID, under the
// contact's domain as a real reply would be.
func (s *Simulator) replyMessageID(target Target, tc TargetContact) string {
	return fmt.Sprintf("<sbx-reply-%s-%s@%s>",
		short(target.CampaignID), short(tc.ContactID), tc.Persona.Domain)
}

// short is the first eight hex characters of a UUID — enough to keep the
// generated Message-IDs unique within a workspace while staying readable when
// someone is reading raw rows to debug a thread.
func short(id uuid.UUID) string { return id.String()[:8] }

// textToHTML renders a plain-text body as paragraph HTML. The thread reader
// prefers the HTML leg, so an inbound message with only text renders blank.
func textToHTML(text string) string {
	var b strings.Builder
	for _, para := range strings.Split(text, "\n\n") {
		if para == "" {
			continue
		}
		b.WriteString("<p>")
		b.WriteString(strings.ReplaceAll(para, "\n", "<br>"))
		b.WriteString("</p>")
	}
	return b.String()
}

// userAgents rotate across the simulated opens and clicks so the tracking
// data does not look like one robot. None matches the prefetch/scanner
// filters the tracking service applies, so these all count as human opens.
var userAgents = []string{
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/127.0.0.0 Safari/537.36",
	"Mozilla/5.0 (iPhone; CPU iPhone OS 17_6 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.6 Mobile/15E148 Safari/604.1",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:129.0) Gecko/20100101 Firefox/129.0",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15",
}

// userAgentFor picks a persona's user agent deterministically, so the same
// contact always "reads mail" on the same device across re-runs.
func userAgentFor(p Persona) string {
	return userAgents[percentile(len(p.Email), "ua")%len(userAgents)]
}
