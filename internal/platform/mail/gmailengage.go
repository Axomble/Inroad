package mail

import (
	"context"
	"fmt"
	"strings"

	gmail "google.golang.org/api/gmail/v1"
)

// Gmail system label ids used by engagement. Removing UNREAD marks a message read;
// removing SPAM (and re-adding INBOX) rescues it out of the spam folder.
const (
	gmailLabelUnread = "UNREAD"
	gmailLabelSpam   = "SPAM"
	gmailLabelInbox  = "INBOX"
)

// GmailEngager performs recipient-side engagement through the Gmail API using a
// per-call access token. No SSRF vetting: the host is Google's fixed API endpoint,
// not user input. The unexported func fields are the wire seam (mirroring
// GmailReader/GmailSender): nil selects the real client call, tests stub them to
// run network-free.
type GmailEngager struct {
	// newServiceFn builds the per-call Gmail service. nil = the real static-token
	// service (gmailService).
	newServiceFn func(ctx context.Context, accessToken string) (*gmail.Service, error)
	// findFn resolves the internal message id(s) for an RFC822 Message-ID via
	// messages.list q="rfc822msgid:...". nil = the real call (gmailFindByMsgID).
	findFn func(ctx context.Context, srv *gmail.Service, messageID string) ([]string, error)
	// modifyFn applies a label add/remove to one message (users.messages.modify).
	// nil = the real call (gmailModifyLabels).
	modifyFn func(ctx context.Context, srv *gmail.Service, msgID string, add, remove []string) error
}

// NewGmailEngager returns a GmailEngager that talks to the real Gmail API.
func NewGmailEngager() *GmailEngager { return &GmailEngager{} }

// gmailMsgIDQuery builds the Gmail search query that matches one RFC822 Message-ID
// exactly (the rfc822msgid: operator). The surrounding angle brackets carried on a
// header value are stripped so the operator matches. It is a pure function so the
// query construction is unit-testable.
//
// SECURITY: the value is carried as the Gmail API's `q` query parameter, which the
// google.golang.org/api client URL-encodes into the request — it is never
// concatenated into a URL or command by hand. The search is confined to the
// recipient's OWN mailbox (users.messages.list("me")), so it can neither reach nor
// mutate another user's mail.
func gmailMsgIDQuery(messageID string) string {
	return "rfc822msgid:" + strings.Trim(messageID, "<>")
}

// MarkRead removes the UNREAD label from every message matching the Message-ID.
func (g *GmailEngager) MarkRead(ctx context.Context, t EngageTarget) error {
	return g.forEachMatch(ctx, t, func(srv *gmail.Service, id string) error {
		return g.modify(ctx, srv, id, nil, []string{gmailLabelUnread})
	})
}

// Rescue removes the SPAM label and re-adds INBOX for every matching message, so
// Gmail routes it back to the inbox ("not spam"). A message not in spam simply has
// no SPAM label to remove — the modify is idempotent.
func (g *GmailEngager) Rescue(ctx context.Context, t EngageTarget) error {
	return g.forEachMatch(ctx, t, func(srv *gmail.Service, id string) error {
		return g.modify(ctx, srv, id, []string{gmailLabelInbox}, []string{gmailLabelSpam})
	})
}

// forEachMatch builds the service, resolves the Message-ID to internal ids, and
// runs fn for each. Zero matches (the message vanished / never arrived) is a clean
// no-op, mirroring the IMAP engager's empty-search behavior.
func (g *GmailEngager) forEachMatch(ctx context.Context, t EngageTarget, fn func(srv *gmail.Service, id string) error) error {
	srv, err := g.newService(ctx, string(t.AccessToken))
	if err != nil {
		return err
	}
	ids, err := g.find(ctx, srv, t.MessageID)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := fn(srv, id); err != nil {
			return err
		}
	}
	return nil
}

func (g *GmailEngager) newService(ctx context.Context, accessToken string) (*gmail.Service, error) {
	if g.newServiceFn != nil {
		return g.newServiceFn(ctx, accessToken)
	}
	return gmailService(ctx, accessToken)
}

func (g *GmailEngager) find(ctx context.Context, srv *gmail.Service, messageID string) ([]string, error) {
	if g.findFn != nil {
		return g.findFn(ctx, srv, messageID)
	}
	return gmailFindByMsgID(ctx, srv, messageID)
}

func (g *GmailEngager) modify(ctx context.Context, srv *gmail.Service, msgID string, add, remove []string) error {
	if g.modifyFn != nil {
		return g.modifyFn(ctx, srv, msgID, add, remove)
	}
	return gmailModifyLabels(ctx, srv, msgID, add, remove)
}

// gmailFindByMsgID lists the internal ids of messages whose RFC822 Message-ID
// matches, via the rfc822msgid: search operator (an exact match — normally 0 or 1
// result).
func gmailFindByMsgID(ctx context.Context, srv *gmail.Service, messageID string) ([]string, error) {
	resp, err := srv.Users.Messages.List("me").
		Q(gmailMsgIDQuery(messageID)).
		Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("gmail: messages.list(rfc822msgid): %w", err)
	}
	ids := make([]string, 0, len(resp.Messages))
	for _, m := range resp.Messages {
		if m != nil && m.Id != "" {
			ids = append(ids, m.Id)
		}
	}
	return ids, nil
}

// gmailModifyLabels applies a label add/remove to one message.
func gmailModifyLabels(ctx context.Context, srv *gmail.Service, msgID string, add, remove []string) error {
	req := &gmail.ModifyMessageRequest{AddLabelIds: add, RemoveLabelIds: remove}
	if _, err := srv.Users.Messages.Modify("me", msgID, req).Context(ctx).Do(); err != nil {
		return fmt.Errorf("gmail: messages.modify: %w", err)
	}
	return nil
}
