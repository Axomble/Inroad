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

// Send picks the transport by Provider. All three legs honor ctx, but not to
// the same depth: the Gmail and Graph API legs are context-aware end to end,
// while the SMTP leg (NetSender.Send) only bounds its SSRF-vetting DNS lookup
// on ctx — the underlying gomail dial/send still runs to its own configured
// timeout, since gomail's DialAndSend has no context-aware entry point.
// Anything other than an API provider takes the SMTP path, so an empty Provider
// stays byte-for-byte the old behavior.
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
