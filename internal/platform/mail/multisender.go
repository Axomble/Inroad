package mail

import "context"

// OutboundJob is the transport-agnostic slice of a send: which provider, and
// the credential for it. Exactly one credential set is populated per Provider
// ("smtp" fills Host/Port/Username/Password/AllowPlaintext; "gmail" and "m365" fill
// AccessToken — both are API transports over a fresh short-lived token).
type OutboundJob struct {
	Provider string // "smtp" | "gmail" | "m365"
	Host     string
	Port     int
	Username string
	Password string
	// AllowPlaintext is the explicit cleartext opt-out for the SMTP leg; its zero
	// value keeps TLS enforced (see SMTPConfig). Both send handlers thread the
	// persisted per-mailbox opt-out (mailboxes.allow_plaintext) through to here,
	// so TLS stays enforced unless a mailbox has explicitly opted out (security
	// Invariant 6).
	AllowPlaintext bool
	AccessToken    string // gmail, m365
}

// MultiSender dispatches a send to the right transport by Provider. It is the
// single place the SMTP/Gmail/Graph branch lives; both worker send handlers
// call it.
type MultiSender struct {
	smtp  *NetSender
	gmail *GmailSender
	graph *GraphSender
}

// NewMultiSender builds the dispatcher over the concrete SMTP, Gmail, and Graph
// senders.
func NewMultiSender(smtp *NetSender, gmail *GmailSender, graph *GraphSender) *MultiSender {
	return &MultiSender{smtp: smtp, gmail: gmail, graph: graph}
}

// Send picks the transport by Provider. All three legs are context-aware end to
// end: the Gmail and Graph API legs through their HTTP clients, and the SMTP leg
// through gomail's DialAndSendWithContext, with gomail's configured timeout as
// the ceiling. (The SMTP leg used to honour ctx only for its SSRF-vetting DNS
// lookup, on the belief that gomail had no context-aware entry point.) Anything
// other than an API provider takes the SMTP path, so an empty Provider stays
// byte-for-byte the old behavior.
func (m *MultiSender) Send(ctx context.Context, tj OutboundJob, msg Message) (string, error) {
	switch tj.Provider {
	case "gmail":
		return m.gmail.Send(ctx, tj.AccessToken, msg)
	case "m365":
		return m.graph.Send(ctx, tj.AccessToken, msg)
	default:
		return m.smtp.Send(ctx, SMTPConfig{
			Host: tj.Host, Port: tj.Port, Username: tj.Username, Password: tj.Password,
			AllowPlaintext: tj.AllowPlaintext,
		}, msg)
	}
}
