package inprocess

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/inbox"
	"github.com/inroad/inroad/internal/coreapi"
)

// StoreInboundMessage satisfies coreapi.InboxCaptureClient. It parses the
// caller's string/pointer-string ids once at this boundary (coreapi's
// cross-process-friendly contract is all strings) rather than pushing
// uuid.Parse into internal/app/inbox, then delegates to
// inbox.Service.RecordReply — the ONE call that upserts the thread and
// inserts the message atomically; see that method's doc for why this never
// splits into two calls.
//
// in.Subject/in.BodyText/in.BodyHTML are free-text business correspondence:
// like any other sensitive content, they are never logged here (or by
// RecordReply/its store) — only ids ever reach a log line on this path.
func (c client) StoreInboundMessage(ctx context.Context, in coreapi.InboxMessageInput) error {
	workspaceID, err := uuid.Parse(in.WorkspaceID)
	if err != nil {
		return err
	}
	mailboxID, err := uuid.Parse(in.MailboxID)
	if err != nil {
		return err
	}
	campaignID, err := optionalUUID(in.CampaignID)
	if err != nil {
		return err
	}
	contactID, err := optionalUUID(in.ContactID)
	if err != nil {
		return err
	}
	thread, err := c.inbox.RecordReply(ctx, inbox.RecordReplyInput{
		WorkspaceID: workspaceID, MailboxID: mailboxID, CampaignID: campaignID, ContactID: contactID,
		RootMessageID: in.RootMessageID, Subject: in.Subject, LastReplyClass: in.ReplyClass,
		Message: inbox.InsertMessageInput{
			Direction: "inbound", MessageID: in.MessageID, FromEmail: in.FromEmail,
			FromName: in.FromName, ToEmail: in.ToEmail, Subject: in.Subject,
			BodyText: in.BodyText, BodyHTML: in.BodyHTML, ReplyClass: in.ReplyClass,
			OccurredAt: in.OccurredAt,
		},
	})
	if err != nil {
		return err
	}

	c.publishInboxMessageCreated(ctx, in.WorkspaceID, thread, in.OccurredAt)
	c.publishReplyClassified(ctx, in.WorkspaceID, thread.ID.String(), in.ReplyClass)

	// Outbound webhook: reply.received. Emitted here for the same reason the
	// realtime events above are — RecordReply is the one call that commits the
	// thread + message atomically, so this is the first point the event is
	// certainly true. A dispatch failure is swallowed by webhook.Emit and never
	// fails the poller task.
	c.emitReplyReceived(ctx, inboundReplyEvent{
		workspaceID: in.WorkspaceID,
		threadID:    thread.ID.String(),
		mailboxID:   in.MailboxID,
		campaignID:  in.CampaignID,
		contactID:   in.ContactID,
		messageID:   in.MessageID,
		fromEmail:   in.FromEmail,
		toEmail:     in.ToEmail,
		subject:     in.Subject,
		bodyText:    in.BodyText,
		replyClass:  in.ReplyClass,
		occurredAt:  in.OccurredAt,
	})
	return nil
}

// publishInboxMessageCreated tells the workspace's open tabs that a reply
// landed. Emitted from here because RecordReply above is the one call that
// upserts the thread and inserts the message in a single transaction, so this is
// the first point where the event is certainly true.
//
// It returns NOTHING, deliberately. The message is already committed; a browser
// that could not be notified is a missed optimisation (the next poll or refetch
// finds it), not a reason to fail a poller task that would then retry and
// re-deliver mail. Errors are the caller's to log, and this path has no logger,
// so a publish failure is swallowed HERE rather than propagated into a retry —
// which is the one place in this codebase where swallowing is the correct
// choice, and why it is spelled out.
//
// The payload is ids only. The client refetches the thread through the normal
// authorized endpoint, so a socket event cannot become a way around the
// permission checks the REST surface applies — and it carries no sender address,
// subject or body, none of which a workspace-wide broadcast should include.
func (c client) publishInboxMessageCreated(ctx context.Context, workspaceID string, thread inbox.Thread, occurredAt time.Time) {
	if c.realtime == nil {
		return
	}
	data := map[string]any{
		"thread_id":  thread.ID.String(),
		"mailbox_id": thread.MailboxID.String(),
		"unread":     thread.Unread,
	}
	// Present only when the reply matched a campaign/contact, so a client can
	// scope an update without a refetch. Nil is normal (an unmatched inbound).
	if thread.CampaignID != nil {
		data["campaign_id"] = thread.CampaignID.String()
	}
	if thread.ContactID != nil {
		data["contact_id"] = thread.ContactID.String()
	}
	// No ActorID: nobody clicked to make an inbound reply arrive. The client's
	// self-echo guard correctly treats an actorless event as "not mine".
	_ = c.PublishRealtime(ctx, coreapi.RealtimeEventInput{
		WorkspaceID: workspaceID,
		Type:        "inbox.message.created",
		SubjectKind: "thread",
		SubjectID:   thread.ID.String(),
		OccurredAt:  occurredAt,
		Data:        data,
	})
}

// publishReplyClassified tells the workspace's open tabs which label the
// classifier put on the thread's latest reply, so an open inbox list moves its
// label chip without a refetch.
//
// Emitted from here rather than from the worker's dispatch, because RecordReply
// above is where the class actually COMMITS (the thread's last_reply_class and
// the message row's reply_class, in one transaction) and the first point a
// thread id exists at all. The worker's later MarkReplied/RecordReplyClass
// writes tag the ENROLLMENT with the same class; publishing there would mean
// threading the thread id through the dispatch for an event that is already
// certainly true here.
//
// Data is the label KEY and nothing else — no subject line, no sender, no body
// snippet: a socket event is a workspace-wide broadcast and must not carry
// correspondence (same rule as publishInboxMessageCreated above). No actor: a
// classifier verdict is nobody's click, so every tab treats it as somebody
// else's.
//
// Returns nothing: the class is already committed, and a broker outage must not
// fail a poller task that would then retry and re-read the mailbox.
func (c client) publishReplyClassified(ctx context.Context, workspaceID, threadID, replyClass string) {
	if c.realtime == nil {
		return
	}
	// An unclassified capture (the input carried no class) is not a verdict —
	// there is nothing to announce, and the client ignores an empty reply_class
	// anyway rather than blanking a chip.
	if replyClass == "" {
		return
	}
	_ = c.PublishRealtime(ctx, coreapi.RealtimeEventInput{
		WorkspaceID: workspaceID,
		Type:        "inbox.reply.classified",
		SubjectKind: "thread",
		SubjectID:   threadID,
		OccurredAt:  time.Now().UTC(),
		Data:        map[string]any{"reply_class": replyClass},
	})
}

// optionalUUID parses a nilable id string at the coreapi boundary: nil (no
// campaign/contact matched) stays nil; a non-nil value must parse as a UUID.
func optionalUUID(id *string) (*uuid.UUID, error) {
	if id == nil {
		return nil, nil
	}
	parsed, err := uuid.Parse(*id)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

var _ coreapi.InboxCaptureClient = client{}
