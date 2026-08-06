package inprocess

import (
	"context"

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
	_, err = c.inbox.RecordReply(ctx, inbox.RecordReplyInput{
		WorkspaceID: workspaceID, MailboxID: mailboxID, CampaignID: campaignID, ContactID: contactID,
		RootMessageID: in.RootMessageID, Subject: in.Subject, LastReplyClass: in.ReplyClass,
		Message: inbox.InsertMessageInput{
			Direction: "inbound", MessageID: in.MessageID, FromEmail: in.FromEmail,
			FromName: in.FromName, ToEmail: in.ToEmail, Subject: in.Subject,
			BodyText: in.BodyText, BodyHTML: in.BodyHTML, ReplyClass: in.ReplyClass,
			OccurredAt: in.OccurredAt,
		},
	})
	return err
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
