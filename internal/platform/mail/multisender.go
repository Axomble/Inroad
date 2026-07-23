package mail

import "context"

// OutboundJob is the transport-agnostic slice of a send: which provider, and
// the credential for it. Exactly one credential set is populated per Provider
// ("smtp" fills Host/Port/Username/Password/UseTLS; "gmail" fills AccessToken).
type OutboundJob struct {
	Provider    string // "smtp" | "gmail"
	Host        string
	Port        int
	Username    string
	Password    string
	UseTLS      bool
	AccessToken string // gmail
}

// MultiSender dispatches a send to the right transport by Provider. It is the
// single place the SMTP/Gmail branch lives; both worker send handlers call it.
type MultiSender struct {
	smtp  *NetSender
	gmail *GmailSender
}

// NewMultiSender builds the dispatcher over the concrete SMTP and Gmail senders.
func NewMultiSender(smtp *NetSender, gmail *GmailSender) *MultiSender {
	return &MultiSender{smtp: smtp, gmail: gmail}
}

// Send picks the transport by Provider. SMTP ignores ctx (the underlying client
// has its own dial/send timeout); Gmail honors it. Anything other than "gmail"
// takes the SMTP path, so an empty Provider stays byte-for-byte the old behavior.
func (m *MultiSender) Send(ctx context.Context, tj OutboundJob, msg Message) (string, error) {
	switch tj.Provider {
	case "gmail":
		return m.gmail.Send(ctx, tj.AccessToken, msg)
	default:
		return m.smtp.Send(SMTPConfig{
			Host: tj.Host, Port: tj.Port, Username: tj.Username, Password: tj.Password, UseTLS: tj.UseTLS,
		}, msg)
	}
}
