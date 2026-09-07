package inprocess

import (
	"context"
	"time"

	"github.com/inroad/inroad/internal/app/webhook"
)

// snippetMax bounds the reply-body preview carried on a reply.received webhook.
// It is a snippet, not the message — the same "ids + minimal display fields"
// discipline realtime events follow.
const snippetMax = 500

// emitWebhook fans one domain event out to the workspace's webhook endpoints. It
// is a no-op when no emitter is wired (webhooks disabled). Called AFTER the
// originating write has committed; webhook.Emit swallows any dispatch failure so
// this can never fail the caller.
func (c client) emitWebhook(ctx context.Context, workspaceID string, e webhook.Event) {
	webhook.Emit(ctx, c.webhookEmitter, workspaceID, e)
}

// emitReplyReceived builds and emits the reply.received event from a matched
// inbound message. The payload builder lives here (the emitting domain knows the
// shape); webhook.Event only frames {id,event,occurred_at,data}.
func (c client) emitReplyReceived(ctx context.Context, in inboundReplyEvent) {
	if c.webhookEmitter == nil {
		return
	}
	c.emitWebhook(ctx, in.workspaceID, webhook.Event{
		Type:       webhook.EventReplyReceived,
		OccurredAt: in.occurredAt,
		Data: map[string]any{
			"reply": map[string]any{
				"id":             in.messageID,
				"from":           in.fromEmail,
				"to":             in.toEmail,
				"subject":        in.subject,
				"snippet":        truncateRunes(in.bodyText, snippetMax),
				"classification": in.replyClass,
				"received_at":    in.occurredAt.UTC().Format(time.RFC3339),
			},
			"thread_id":   in.threadID,
			"campaign_id": nilString(in.campaignID),
			"contact_id":  nilString(in.contactID),
			"mailbox_id":  in.mailboxID,
		},
	})
}

// inboundReplyEvent carries what emitReplyReceived needs, kept as a struct so the
// call site (StoreInboundMessage) reads as one assignment rather than nine
// positional args.
type inboundReplyEvent struct {
	workspaceID string
	threadID    string
	mailboxID   string
	campaignID  *string
	contactID   *string
	messageID   string
	fromEmail   string
	toEmail     string
	subject     string
	bodyText    string
	replyClass  string
	occurredAt  time.Time
}

// emitEmailBounced emits the email.bounced event for a hard bounce.
//
// NOTE: coreapi.MarkBounced's signature carries only the enrollment id and the
// address, so campaign_id / contact_id / mailbox_id are not available here yet —
// enriching the payload with them needs a widening of that seam and is a
// documented follow-up. The event still fires with the fields that identify the
// bounce (address, type, enrollment).
func (c client) emitEmailBounced(ctx context.Context, workspaceID, email, enrollmentID string) {
	if c.webhookEmitter == nil {
		return
	}
	now := c.now().UTC()
	data := map[string]any{
		"bounce": map[string]any{
			"type":        "hard",
			"diagnostic":  "",
			"reported_at": now.Format(time.RFC3339),
		},
		"email": email,
	}
	if enrollmentID != "" {
		data["enrollment_id"] = enrollmentID
	}
	c.emitWebhook(ctx, workspaceID, webhook.Event{
		Type:       webhook.EventEmailBounced,
		OccurredAt: now,
		Data:       data,
	})
}

// emitContactUnsubscribed emits the contact.unsubscribed event. source is
// "reply" for a reply-classified opt-out and "one_click" for the RFC 8058
// endpoint.
func (c client) emitContactUnsubscribed(ctx context.Context, workspaceID, email, source string) {
	if c.webhookEmitter == nil {
		return
	}
	now := c.now().UTC()
	c.emitWebhook(ctx, workspaceID, webhook.Event{
		Type:       webhook.EventContactUnsubscribed,
		OccurredAt: now,
		Data: map[string]any{
			"contact_id":  nil,
			"email":       email,
			"reason":      "unsubscribe",
			"source":      source,
			"occurred_at": now.Format(time.RFC3339),
		},
	})
}

func truncateRunes(s string, maxLen int) string {
	r := []rune(s)
	if len(r) <= maxLen {
		return s
	}
	return string(r[:maxLen])
}

func nilString(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}
