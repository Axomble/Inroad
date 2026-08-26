package main

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// The inbox seed exists so the inbox screen renders the way it will in
// production the first time someone opens it: threads in every time bucket
// (today through months old), every built-in reply class, a read/unread mix,
// multi-message conversations, category labels, and a couple of snoozes.
// An inbox seeded with three same-day threads hides all of that.

// inboxSeedMessage is one leg of a seeded conversation. `AgeHours` is how long
// before the THREAD's own age anchor the message occurred, so a conversation
// reads oldest-first with realistic gaps.
type inboxSeedMessage struct {
	Outbound bool
	AgeHours float64
	Body     string
}

// inboxSeedThread describes one thread. `AgeHours` places its newest message
// that many hours before the seed run, which is what buckets it under Today /
// Yesterday / this week / this month / older in the UI.
type inboxSeedThread struct {
	Contact  int // index into the seeded contacts
	Mailbox  int // index into the seeded mailboxes
	Subject  string
	Class    string // one of the built-in reply-label keys (migration 000047)
	AgeHours float64
	Unread   bool
	Labels   []string // names from seedInboxLabels
	Snoozed  bool
	Messages []inboxSeedMessage
}

// inboxSeedLabels are the operator categories, created first so threads can
// reference them by name.
var inboxSeedLabels = []struct{ Name, Color string }{
	{"Demo booked", "#0a7d4f"},
	{"Follow up", "#b85600"},
	{"Pricing", "#6d55d9"},
}

// inboxSeedThreads is handcrafted rather than generated: the top of an inbox
// is the first thing a reader judges, so the visible conversations have to
// read like real prospects, not like "Subject 17".
var inboxSeedThreads = []inboxSeedThread{
	{
		Contact: 0, Mailbox: 0, Subject: "Re: A quick idea for Northwind",
		Class: "positive", AgeHours: 0.6, Unread: true, Labels: []string{"Demo booked"},
		Messages: []inboxSeedMessage{
			{Outbound: true, AgeHours: 49, Body: "Hi Ana,\n\nSaw what Northwind is building around fleet telemetry. We help seed-stage teams get their first 20 customer conversations without hiring an SDR.\n\nWorth a short call this week?"},
			{AgeHours: 26, Body: "Hi,\n\nInteresting timing, we were just talking about this. How does it handle deliverability on a brand-new domain?"},
			{Outbound: true, AgeHours: 24, Body: "Great question. New domains ramp automatically: warmup runs for 30 days and sending caps rise as reputation builds. Happy to show you the dashboard.\n\nDoes Thursday 10:00 work?"},
			{AgeHours: 0.6, Body: "Thursday works. Send an invite and we'll get our head of growth in the room too."},
		},
	},
	{
		Contact: 1, Mailbox: 0, Subject: "Re: A quick idea for Lumen",
		Class: "neutral", AgeHours: 2.5, Unread: true,
		Messages: []inboxSeedMessage{
			{Outbound: true, AgeHours: 30, Body: "Hi Liam,\n\nSaw what Lumen is building. Would getting more qualified replies per week help right now?"},
			{AgeHours: 2.5, Body: "Maybe. We already run outbound through another tool, what would switching actually get us? Send over something I can read, no calls yet."},
		},
	},
	{
		Contact: 2, Mailbox: 1, Subject: "Re: Following up on the warmup question",
		Class: "positive", AgeHours: 5, Unread: true, Labels: []string{"Pricing"},
		Messages: []inboxSeedMessage{
			{Outbound: true, AgeHours: 74, Body: "Hi Yuki,\n\nCircling back on the warmup question from last week. The short version: every mailbox warms on a schedule tied to its own bounce and open history."},
			{AgeHours: 5, Body: "Thanks, this answers it. What does pricing look like for 10 mailboxes? If it's sane we'd start next month."},
		},
	},
	{
		Contact: 3, Mailbox: 0, Subject: "Automatic reply: A quick idea for Sablepoint",
		Class: "out_of_office", AgeHours: 8, Unread: false,
		Messages: []inboxSeedMessage{
			{Outbound: true, AgeHours: 9, Body: "Hi Noor,\n\nQuick idea for Sablepoint's onboarding emails."},
			{AgeHours: 8, Body: "I am out of the office until Monday, 31 August, with limited access to email. For urgent matters please contact ops@sablepoint.example.com.\n\nNoor Haddad"},
		},
	},
	{
		Contact: 4, Mailbox: 1, Subject: "Re: A quick idea for Arcadia",
		Class: "negative", AgeHours: 27, Unread: false,
		Messages: []inboxSeedMessage{
			{Outbound: true, AgeHours: 52, Body: "Hi Theo,\n\nSaw what Arcadia is building. Worth a chat about how you're sourcing your first design-partner conversations?"},
			{AgeHours: 27, Body: "Not for us. We're B2C and everything inbound, cold email isn't a channel we'd invest in. Good luck with it."},
		},
	},
	{
		Contact: 5, Mailbox: 0, Subject: "Re: Intro from the SaaStr list",
		Class: "positive", AgeHours: 31, Unread: true, Labels: []string{"Follow up"},
		Messages: []inboxSeedMessage{
			{Outbound: true, AgeHours: 55, Body: "Hi Mira,\n\nWe met briefly at SaaStr. You mentioned reply rates falling off after the first step, we built exactly for that."},
			{AgeHours: 31, Body: "I remember! Send me the case study you mentioned and let's pick a time in two weeks, this week is a write-off."},
		},
	},
	{
		Contact: 6, Mailbox: 1, Subject: "Please remove me from this list",
		Class: "unsubscribe", AgeHours: 50, Unread: false,
		Messages: []inboxSeedMessage{
			{Outbound: true, AgeHours: 51, Body: "Hi Kai,\n\nQuick idea for Northwind's outbound."},
			{AgeHours: 50, Body: "Unsubscribe. Please remove this address from all future mailings."},
		},
	},
	{
		Contact: 7, Mailbox: 0, Subject: "Re: A quick idea for Lumen",
		Class: "neutral", AgeHours: 76, Unread: false,
		Messages: []inboxSeedMessage{
			{Outbound: true, AgeHours: 99, Body: "Hi Sofia,\n\nWould a steadier stream of qualified replies help Lumen's pipeline this quarter?"},
			{AgeHours: 76, Body: "We're mid-fundraise, so nothing new this quarter. Ping me again in October and we can look properly."},
		},
	},
	{
		Contact: 8, Mailbox: 0, Subject: "Re: Your note about deliverability",
		Class: "positive", AgeHours: 98, Unread: false, Labels: []string{"Demo booked", "Pricing"}, Snoozed: true,
		Messages: []inboxSeedMessage{
			{Outbound: true, AgeHours: 122, Body: "Hi Ana,\n\nYou asked how we keep bounce rates under control across a pool of mailboxes. Wrote it up here in three paragraphs."},
			{AgeHours: 98, Body: "Good write-up. Let's do a technical deep-dive with our platform team, sometime after the 5th. What's your pricing at 25 seats?"},
		},
	},
	{
		Contact: 9, Mailbox: 1, Subject: "Delivery Status Notification (Failure)",
		Class: "auto_reply", AgeHours: 120, Unread: false,
		Messages: []inboxSeedMessage{
			{Outbound: true, AgeHours: 121, Body: "Hi Liam,\n\nQuick idea for Fernhill."},
			{AgeHours: 120, Body: "This is an automatically generated message.\n\nDelivery to the following recipient failed permanently: liam.novak9@fernhill.example.com\n\nReason: 550 5.1.1 The email account does not exist."},
		},
	},
	{
		Contact: 10, Mailbox: 0, Subject: "Re: A quick idea for Northwind",
		Class: "unknown", AgeHours: 200, Unread: false,
		Messages: []inboxSeedMessage{
			{Outbound: true, AgeHours: 225, Body: "Hi Yuki,\n\nSaw what Northwind is building. Worth a short call?"},
			{AgeHours: 200, Body: "who is this and how did you get this address"},
		},
	},
	{
		Contact: 11, Mailbox: 1, Subject: "Re: Getting off spreadsheets for outbound",
		Class: "positive", AgeHours: 320, Unread: false, Labels: []string{"Follow up"},
		Messages: []inboxSeedMessage{
			{Outbound: true, AgeHours: 350, Body: "Hi Noor,\n\nStill running outbound from spreadsheets? There's a cheaper hour in your week if so."},
			{AgeHours: 320, Body: "Painfully accurate. We trialled two tools and hated both. What makes yours different? Genuinely asking."},
		},
	},
	{
		Contact: 12, Mailbox: 0, Subject: "Re: A quick idea for Sablepoint",
		Class: "negative", AgeHours: 410, Unread: false,
		Messages: []inboxSeedMessage{
			{Outbound: true, AgeHours: 434, Body: "Hi Theo,\n\nQuick idea for Sablepoint's founder-led sales motion."},
			{AgeHours: 410, Body: "We've got this covered internally, thanks. No need to follow up."},
		},
	},
	{
		Contact: 13, Mailbox: 1, Subject: "Re: Warm intro request",
		Class: "neutral", AgeHours: 500, Unread: false, Snoozed: true,
		Messages: []inboxSeedMessage{
			{Outbound: true, AgeHours: 530, Body: "Hi Mira,\n\nWould an intro to how we run ramped sending help Arcadia's new domain?"},
			{AgeHours: 500, Body: "Possibly. We're rebuilding the site first, so email is on hold. Try me next month."},
		},
	},
	{
		Contact: 14, Mailbox: 0, Subject: "Automatic reply: Out of office",
		Class: "out_of_office", AgeHours: 750, Unread: false,
		Messages: []inboxSeedMessage{
			{Outbound: true, AgeHours: 751, Body: "Hi Kai,\n\nQuick idea for Fernhill's outbound."},
			{AgeHours: 750, Body: "Thank you for your email. I am on parental leave until further notice. Your message will not be forwarded.\n\nKai Okafor"},
		},
	},
	{
		Contact: 15, Mailbox: 1, Subject: "Re: A quick idea for Arcadia",
		Class: "positive", AgeHours: 1100, Unread: false,
		Messages: []inboxSeedMessage{
			{Outbound: true, AgeHours: 1124, Body: "Hi Sofia,\n\nSaw what Arcadia is building around procurement workflows. Worth a chat?"},
			{AgeHours: 1100, Body: "This landed at the right time last quarter and we never closed the loop, apologies. Still happy to talk if the offer stands."},
		},
	},
	{
		Contact: 16, Mailbox: 0, Subject: "Re: Question about your sending caps",
		Class: "neutral", AgeHours: 1600, Unread: false,
		Messages: []inboxSeedMessage{
			{Outbound: true, AgeHours: 1626, Body: "Hi Ana,\n\nFollowing up on the caps question: every mailbox carries its own daily cap and minimum send interval."},
			{AgeHours: 1600, Body: "Understood. We ended up parking the project, but keep us on the list for the beta of the deliverability reports."},
		},
	},
	{
		Contact: 17, Mailbox: 1, Subject: "Re: A quick idea for Lumen",
		Class: "negative", AgeHours: 2200, Unread: false,
		Messages: []inboxSeedMessage{
			{Outbound: true, AgeHours: 2225, Body: "Hi Liam,\n\nQuick idea for Lumen's outbound."},
			{AgeHours: 2200, Body: "No thanks. Also your second paragraph has a typo, 'recieve'. Free tip."},
		},
	},
}

// seedInbox writes the threads above. Timestamps are the tricky part:
// UpsertInboxThread stamps last_message_at = now() by design (the poller only
// ever sees a message as it arrives), so after inserting each conversation the
// thread row is re-anchored into the past with a direct UPDATE — the one
// place the seed goes around the query layer, because no runtime code path
// legitimately back-dates a thread.
func seedInbox(
	ctx context.Context,
	q *gen.Queries,
	pool *pgxpool.Pool,
	ws, user uuid.UUID,
	mailboxes []uuid.UUID,
	contacts []seededContact,
) (int, error) {
	labelByName := make(map[string]uuid.UUID, len(inboxSeedLabels))
	for _, l := range inboxSeedLabels {
		created, err := q.CreateInboxLabel(ctx, gen.CreateInboxLabelParams{WorkspaceID: ws, Name: l.Name, Color: l.Color})
		if err != nil {
			return 0, fmt.Errorf("label %q: %w", l.Name, err)
		}
		labelByName[l.Name] = created.ID
	}

	// One shared anchor so every age is measured from the same instant and the
	// seeded inbox's relative order is exactly the spec's.
	now := time.Now().UTC()

	for i, spec := range inboxSeedThreads {
		contact := contacts[spec.Contact%len(contacts)]
		mailbox := mailboxes[spec.Mailbox%len(mailboxes)]
		mailboxEmail := seedMailboxEmails[spec.Mailbox%len(seedMailboxEmails)]

		thread, err := q.UpsertInboxThread(ctx, gen.UpsertInboxThreadParams{
			WorkspaceID:    ws,
			MailboxID:      mailbox,
			ContactID:      pgtype.UUID{Bytes: contact.ID, Valid: true},
			RootMessageID:  fmt.Sprintf("<seed-thread-%d@inroad.test>", i),
			Subject:        spec.Subject,
			LastReplyClass: spec.Class,
		})
		if err != nil {
			return 0, fmt.Errorf("thread %d: %w", i, err)
		}

		for j, msg := range spec.Messages {
			occurred := now.Add(-time.Duration(msg.AgeHours * float64(time.Hour)))
			params := gen.InsertInboxMessageParams{
				ThreadID:    thread.ID,
				WorkspaceID: ws,
				MessageID:   fmt.Sprintf("<seed-msg-%d-%d@inroad.test>", i, j),
				Subject:     spec.Subject,
				BodyText:    msg.Body,
				OccurredAt:  pgtype.Timestamptz{Time: occurred, Valid: true},
			}
			if msg.Outbound {
				params.Direction = "outbound"
				params.FromEmail = mailboxEmail
				params.ToEmail = contact.Email
			} else {
				params.Direction = "inbound"
				params.FromEmail = contact.Email
				params.FromName = contact.First + " " + contact.Last
				params.ToEmail = mailboxEmail
				params.ReplyClass = spec.Class
			}
			if err := q.InsertInboxMessage(ctx, params); err != nil {
				return 0, fmt.Errorf("thread %d message %d: %w", i, j, err)
			}
		}

		anchoredAt := now.Add(-time.Duration(spec.AgeHours * float64(time.Hour)))
		if _, err := pool.Exec(ctx,
			`UPDATE inbox_threads SET last_message_at = $1, unread = $2 WHERE id = $3 AND workspace_id = $4`,
			anchoredAt, spec.Unread, thread.ID, ws,
		); err != nil {
			return 0, fmt.Errorf("thread %d anchor: %w", i, err)
		}

		for _, name := range spec.Labels {
			if err := q.AssignInboxThreadLabel(ctx, gen.AssignInboxThreadLabelParams{
				ThreadID: thread.ID, LabelID: labelByName[name], WorkspaceID: ws,
			}); err != nil {
				return 0, fmt.Errorf("thread %d label %q: %w", i, name, err)
			}
		}

		if spec.Snoozed {
			if _, err := q.UpsertInboxThreadSnooze(ctx, gen.UpsertInboxThreadSnoozeParams{
				ThreadID:    thread.ID,
				WorkspaceID: ws,
				SnoozeUntil: pgtype.Timestamptz{Time: now.Add(72 * time.Hour), Valid: true},
				SnoozedBy:   pgtype.UUID{Bytes: user, Valid: true},
			}); err != nil {
				return 0, fmt.Errorf("thread %d snooze: %w", i, err)
			}
		}
	}
	return len(inboxSeedThreads), nil
}

// seedMailboxEmails mirrors seedMailboxes' specs by index, so a message's
// from/to lines name the same address the mailbox screen shows.
var seedMailboxEmails = []string{
	"alex@outbound.example.com",
	"jordan@outbound.example.com",
	"sam@outbound.example.com",
}
