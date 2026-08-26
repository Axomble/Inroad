package sandbox

import (
	"context"
	"fmt"
	"strings"
	"time"

	gomail "github.com/wneessen/go-mail"
)

// Deliverer optionally puts a simulated message onto a real SMTP hop. The
// harness records its history in Postgres either way; delivering as well is
// what lets someone watch mail arrive in Mailpit and read the actual rendered
// message, headers and all.
//
// An interface at this seam (rather than a concrete client) so the Simulator
// can be unit-tested with a recorder and so a self-hoster can point the
// harness at any catcher that speaks SMTP.
type Deliverer interface {
	Deliver(ctx context.Context, msg OutboundMessage) error
}

// OutboundMessage is one simulated email to put on the wire. MessageID and
// References are carried explicitly because they are what make a thread a
// thread — letting the library mint its own Message-ID would break the
// correspondence between the delivered mail and the seeded rows.
type OutboundMessage struct {
	FromEmail  string
	FromName   string
	ToEmail    string
	ToName     string
	Subject    string
	BodyText   string
	MessageID  string
	References string
	Date       time.Time
}

// SMTPDeliverer delivers to a plain, unauthenticated local SMTP catcher —
// Mailpit in the dev compose stack (deploy/compose/docker-compose.dev.yml,
// SMTP on :1025, UI on :8025).
//
// It speaks cleartext with no auth unconditionally, which is safe ONLY
// because it is unreachable from the guarded paths: the sandbox harness
// refuses to run outside a non-production environment (see Guard), and this
// type is constructed nowhere else. It deliberately does NOT reuse
// platform/notify's smtpSender, whose TLS policy is a production security
// invariant that should not grow a "but not here" branch.
// The connection is dialed ONCE, lazily, and reused for the whole run. A
// dial per message is what the first cut did, and at a few hundred messages
// the handshake cost dominated everything else — a run that should take
// seconds took minutes, which for a "one command" tool is a defect rather
// than a nuisance. Not safe for concurrent use, which matches its only
// caller: the Simulator replays contacts sequentially.
type SMTPDeliverer struct {
	host   string
	port   int
	client *gomail.Client
}

// NewSMTPDeliverer builds a deliverer for host:port. It does not connect;
// the first Deliver does.
func NewSMTPDeliverer(host string, port int) *SMTPDeliverer {
	return &SMTPDeliverer{host: host, port: port}
}

var _ Deliverer = (*SMTPDeliverer)(nil)

// connect dials on first use and hands back the shared client.
func (d *SMTPDeliverer) connect(ctx context.Context) (*gomail.Client, error) {
	if d.client != nil {
		return d.client, nil
	}
	client, err := gomail.NewClient(d.host,
		gomail.WithPort(d.port),
		gomail.WithTimeout(15*time.Second),
		// Mailpit advertises no AUTH and speaks cleartext; see the type's doc
		// for why that is acceptable only on this path.
		gomail.WithSMTPAuth(gomail.SMTPAuthNoAuth),
		gomail.WithTLSPolicy(gomail.NoTLS),
	)
	if err != nil {
		return nil, fmt.Errorf("smtp client for %s:%d: %w", d.host, d.port, err)
	}
	if err := client.DialWithContext(ctx); err != nil {
		return nil, fmt.Errorf("dial %s:%d: %w", d.host, d.port, err)
	}
	d.client = client
	return client, nil
}

// Close releases the shared connection. Safe to call when nothing was ever
// dialed, so a caller can defer it unconditionally.
func (d *SMTPDeliverer) Close() error {
	if d.client == nil {
		return nil
	}
	client := d.client
	d.client = nil
	if err := client.Close(); err != nil {
		return fmt.Errorf("close smtp connection: %w", err)
	}
	return nil
}

// DefaultMailpitHost / DefaultMailpitSMTPPort match the dev compose stack's
// Mailpit service, so the harness needs no flags to find it in the documented
// setup.
//
// The host is 127.0.0.1 rather than "localhost" deliberately. Docker publishes
// the port on IPv4 only, while "localhost" resolves to ::1 first on Windows —
// so the literal is the difference between the documented command working out
// of the box and failing with "the target machine actively refused it".
const (
	DefaultMailpitHost     = "127.0.0.1"
	DefaultMailpitSMTPPort = 1025
)

// Deliver sends one message. Every failure is wrapped with the recipient, so
// a run that cannot reach the catcher says which message it died on rather
// than just "connection refused".
func (d *SMTPDeliverer) Deliver(ctx context.Context, m OutboundMessage) error {
	msg := gomail.NewMsg()
	if err := msg.FromFormat(m.FromName, m.FromEmail); err != nil {
		return fmt.Errorf("from %q: %w", m.FromEmail, err)
	}
	if err := msg.AddToFormat(m.ToName, m.ToEmail); err != nil {
		return fmt.Errorf("to %q: %w", m.ToEmail, err)
	}
	msg.Subject(m.Subject)
	msg.SetBodyString(gomail.TypeTextPlain, m.BodyText)
	msg.AddAlternativeString(gomail.TypeTextHTML, textToHTML(m.BodyText))

	// The Message-ID is set from the seeded value, with the angle brackets
	// stripped: go-mail adds them back, and passing them through would emit a
	// doubled "<<id>>" that no client would thread on.
	msg.SetMessageIDWithValue(trimAngles(m.MessageID))
	if m.References != "" {
		msg.SetGenHeader(gomail.HeaderReferences, m.References)
		msg.SetGenHeader(gomail.HeaderInReplyTo, m.References)
	}
	if !m.Date.IsZero() {
		msg.SetDateWithValue(m.Date)
	}

	client, err := d.connect(ctx)
	if err != nil {
		return err
	}
	if err := client.Send(msg); err != nil {
		return fmt.Errorf("deliver to %s: %w", m.ToEmail, err)
	}
	return nil
}

// trimAngles strips the RFC 5322 angle brackets from a Message-ID, which is
// how it is stored in sends.message_id and how the inbox matches replies.
func trimAngles(id string) string {
	return strings.TrimSuffix(strings.TrimPrefix(id, "<"), ">")
}
