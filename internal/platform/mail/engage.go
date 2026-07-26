package mail

import (
	"context"
	"errors"
)

// ErrEngageUnsupported signals that recipient-side engagement (mark-read /
// rescue-from-spam) is not implemented for a provider. It is NOT a failure: the
// warmup:engage handler logs it as a clean skip and continues (the reply send,
// which uses the ordinary send transport, still works). v1 returns it for the
// Graph/M365 provider — Graph engagement is a documented follow-up. Callers test
// it with errors.Is.
var ErrEngageUnsupported = errors.New("mail: engagement unsupported for provider")

// EngageTarget is one received warmup message a recipient should act on. The
// transport is the RECIPIENT's own mailbox credential (engagement operates on the
// recipient's own inbox): IMAP fields drive the smtp/imap engager, AccessToken the
// Gmail engager. IMAPPassword/AccessToken are []byte so the worker zeroizes them
// after use.
//
// SourceFolder and MessageID come from the C5a receipt and are ATTACKER-
// INFLUENCEABLE inbound message content (the folder was resolved from the server's
// own LIST, the Message-ID is copied verbatim from the received header). Every
// engager MUST pass them as properly-quoted/literal protocol arguments, never
// string-concatenated into a raw command (see searchByMessageID / the go-imap
// mailbox-name literal encoding, and gmailMsgIDQuery).
type EngageTarget struct {
	Provider       string // "smtp" | "gmail" | "m365"
	AccessToken    []byte // gmail/m365 bearer
	IMAPHost       string
	IMAPPort       int
	IMAPUsername   string
	IMAPPassword   []byte
	AllowPlaintext bool
	SourceFolder   string // actual provider folder the message was found in (INBOX / a junk folder)
	MessageID      string // RFC822 Message-ID of the received message
}

// Engager performs the recipient-side actions on a received warmup message. The
// warmup:engage worker depends on this interface (injected), never a concrete
// dialer, so it is unit-testable with a fake. Both methods are idempotent: acting
// on a message that is already read / already in the inbox is a clean no-op.
type Engager interface {
	// MarkRead clears the unread flag on the message.
	MarkRead(ctx context.Context, t EngageTarget) error
	// Rescue moves the message out of its spam/junk folder into the inbox ("not
	// spam"). A no-op when the message is already in the inbox.
	Rescue(ctx context.Context, t EngageTarget) error
}

// MultiEngager dispatches engagement to the right transport by Provider, mirroring
// MultiSender: smtp/imap mailboxes go through the SSRF-guarded IMAP engager, Gmail
// through the fixed-host API engager, and Graph/M365 returns ErrEngageUnsupported
// (v1 follow-up). It is the single place the provider branch lives.
type MultiEngager struct {
	imap  *NetEngager
	gmail *GmailEngager
}

// NewMultiEngager builds the dispatcher over the IMAP and Gmail engagers.
func NewMultiEngager(imap *NetEngager, gmail *GmailEngager) *MultiEngager {
	return &MultiEngager{imap: imap, gmail: gmail}
}

// MarkRead routes to the provider's engager; m365 is unsupported in v1.
func (m *MultiEngager) MarkRead(ctx context.Context, t EngageTarget) error {
	switch t.Provider {
	case "gmail":
		return m.gmail.MarkRead(ctx, t)
	case "m365":
		return ErrEngageUnsupported
	default:
		return m.imap.MarkRead(ctx, t)
	}
}

// Rescue routes to the provider's engager; m365 is unsupported in v1.
func (m *MultiEngager) Rescue(ctx context.Context, t EngageTarget) error {
	switch t.Provider {
	case "gmail":
		return m.gmail.Rescue(ctx, t)
	case "m365":
		return ErrEngageUnsupported
	default:
		return m.imap.Rescue(ctx, t)
	}
}
