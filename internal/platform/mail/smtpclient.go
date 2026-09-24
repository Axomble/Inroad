package mail

import (
	"context"
	"fmt"
	"net"
	netmail "net/mail"
	"strings"
	"time"

	gomail "github.com/wneessen/go-mail"
)

// maxDNSLabel and maxDNSName bound a HELO name to what DNS permits, so a
// malformed sender address can never produce an EHLO argument a server has to
// reject on length alone.
const (
	maxDNSLabel = 63
	maxDNSName  = 253
)

// smtpAuthMechanisms is the SMTP AUTH preference order, most preferred first,
// and go-mail applies it only to mechanisms the server ADVERTISES — falling back
// to its own auto-discovery (which also knows SCRAM and NTLM) when the server
// offers none of these.
//
// The previous hardcoded SMTPAuthPlain is a mailbox that silently fails: a
// server offering only AUTH LOGIN — Exchange front ends, cPanel, a good deal of
// shared hosting — rejects every send with "server does not support SMTP AUTH
// type: PLAIN", forever, with nothing an operator can change.
//
// AllowPlaintext is this package's explicit cleartext opt-out (see SMTPConfig),
// and it has to be threaded here: go-mail's PLAIN and LOGIN refuse to put a
// credential on an unencrypted connection unless asked through the -NOENC
// variants. Without them a mailbox that deliberately opted into cleartext could
// authenticate with CRAM-MD5 and nothing else. Mailboxes that have NOT opted out
// — the default, and every TLS-enforced connection — never get the -NOENC forms,
// so the opt-out stays the only way a credential goes out in the clear.
func smtpAuthMechanisms(cfg SMTPConfig) []gomail.SMTPAuthType {
	if cfg.AllowPlaintext {
		return []gomail.SMTPAuthType{
			gomail.SMTPAuthCramMD5, gomail.SMTPAuthLoginNoEnc, gomail.SMTPAuthPlainNoEnc,
		}
	}
	return []gomail.SMTPAuthType{
		gomail.SMTPAuthCramMD5, gomail.SMTPAuthLogin, gomail.SMTPAuthPlain,
	}
}

// newSMTPClient builds the gomail client both the sender and the connection
// tester dial with, so the two cannot disagree about whether a mailbox works.
// They used to: the tester spoke stdlib net/smtp with smtp.PlainAuth and skipped
// AUTH entirely when the username was empty, while the sender spoke gomail and
// always authenticated — so an unauthenticated relay passed its connection test
// and then failed every send.
//
// helo is the EHLO/HELO name; empty leaves go-mail's os.Hostname() default.
// dialFn must return a connection to the already-vetted address (see vetAddr):
// gomail is given the hostname only as the TLS ServerName and never resolves it.
func newSMTPClient(cfg SMTPConfig, timeout time.Duration, helo string, dialFn gomail.DialContextFunc) (*gomail.Client, error) {
	opts := []gomail.Option{
		gomail.WithPort(cfg.Port),
		gomail.WithUsername(cfg.Username),
		gomail.WithPassword(cfg.Password),
		gomail.WithTimeout(timeout),
		gomail.WithDialContextFunc(dialFn),
	}
	if cfg.Username == "" {
		// An open internal relay. Asking for AUTH here fails against a server
		// that advertises none, which is exactly the configuration this is.
		opts = append(opts, gomail.WithSMTPAuth(gomail.SMTPAuthNoAuth))
	} else {
		opts = append(opts, gomail.WithOpportunisticSMTPAuth(smtpAuthMechanisms(cfg)...))
	}
	if helo != "" {
		opts = append(opts, gomail.WithHELO(helo))
	}
	switch {
	case cfg.Port == 465:
		opts = append(opts, gomail.WithSSLPort(false))
	case cfg.AllowPlaintext:
		// Explicit, deliberate cleartext opt-out (rare internal relay).
		opts = append(opts, gomail.WithTLSPolicy(gomail.NoTLS))
	default:
		// Secure default: STARTTLS required on 25/587/2525.
		opts = append(opts, gomail.WithTLSPolicy(gomail.TLSMandatory))
	}

	client, err := gomail.NewClient(cfg.Host, opts...)
	if err != nil {
		return nil, fmt.Errorf("smtp client: %w", err)
	}
	return client, nil
}

// smtpHELO resolves the EHLO name for a connection: the caller's explicit
// EHLODomain when it yields a usable name, else the domain of the address the
// message is from. Empty means "keep go-mail's os.Hostname() default".
func smtpHELO(cfg SMTPConfig, fromEmail string) string {
	if d := heloDomain(cfg.EHLODomain); d != "" {
		return d
	}
	return heloDomain(fromEmail)
}

// heloDomain resolves the domain to greet with. It accepts either an email
// address (any form net/mail parses, plus the bare "local@domain") or a bare
// domain, because its two callers hold different things: a send has the
// envelope sender, a connection test has only SMTPConfig.EHLODomain.
//
// SECURITY: the input is workspace-controlled — a mailbox address or a message's
// From — and go-mail writes the result onto the wire verbatim as the EHLO
// argument. A name containing CRLF would therefore be SMTP command injection, so
// this validates the RESULT as a DNS name (letters, digits, hyphen, dot; labels
// that neither start nor end with a hyphen; length limits) rather than merely
// stripping the characters it happens to think of. Anything else yields "".
//
// A name with no dot is rejected too, for a different reason: an unqualified
// HELO is what Postfix's reject_non_fqdn_helo_hostname and most commercial
// filters penalise, so greeting as "localhost" would be worse than greeting as
// the default.
func heloDomain(address string) string {
	candidate := strings.TrimSpace(address)
	// A display-name form ("Rep <rep@example.com>") needs a real parse; a bare
	// domain or a plain address does not, and ParseAddress rejects both.
	if addr, err := netmail.ParseAddress(candidate); err == nil {
		candidate = addr.Address
	}
	if at := strings.LastIndex(candidate, "@"); at >= 0 {
		candidate = candidate[at+1:]
	}
	domain := strings.TrimSuffix(strings.ToLower(candidate), ".")
	if !isDNSName(domain) {
		return ""
	}
	return domain
}

// isDNSName reports whether s is a syntactically valid, fully qualified DNS
// name. It is deliberately strict: this value is written to the wire.
func isDNSName(s string) bool {
	if s == "" || len(s) > maxDNSName || !strings.Contains(s, ".") {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if label == "" || len(label) > maxDNSLabel {
			return false
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			ch := label[i]
			switch {
			case ch >= 'a' && ch <= 'z', ch >= '0' && ch <= '9', ch == '-':
			default:
				return false
			}
		}
	}
	return true
}

// smtpDialFunc returns the gomail dial hook for an already-vetted address: it
// ignores gomail's own address argument (built from cfg.Host) and always dials
// the pre-vetted ip:port, so hostname re-resolution cannot slip in between the
// SSRF vet and the connection.
func smtpDialFunc(addr string, dialer *net.Dialer) gomail.DialContextFunc {
	return func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dialSMTPTransport(ctx, addr, dialer)
	}
}
