package mail

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"time"

	"github.com/emersion/go-imap/client"
)

// NetTester is the production ConnectionTester that dials real SMTP/IMAP
// servers. It applies SSRF protection (see vetAddr): dangerous/internal targets
// are always rejected; private RFC1918/ULA ranges are rejected unless
// AllowPrivate is set (self-hosted operators reaching internal mail servers).
type NetTester struct {
	Timeout      time.Duration
	AllowPrivate bool
}

// NewNetTester returns a NetTester with a sane default dial timeout.
// allowPrivate permits RFC1918/ULA hosts (default for self-hosted Core; Cloud
// deployments pass false).
func NewNetTester(allowPrivate bool) *NetTester {
	return &NetTester{Timeout: 15 * time.Second, AllowPrivate: allowPrivate}
}

// defaultIMAPTimeout bounds dialIMAP when the caller leaves Timeout unset (its
// zero value), so a hung IMAP server can never block a caller forever.
const defaultIMAPTimeout = 30 * time.Second

// TestSMTP dials the SMTP server, greets it, negotiates TLS, and authenticates —
// without sending any mail. TLS is enforced by default (security Invariant 6):
// port 465 uses implicit TLS, every other port requires STARTTLS — cleartext
// auth is permitted ONLY when cfg.AllowPlaintext is explicitly set.
//
// It dials through newSMTPClient, the SAME client NetSender.Send uses, so a
// passing connection test means a send would get as far as DATA. That was not
// true before: this ran on stdlib net/smtp with smtp.PlainAuth and skipped AUTH
// entirely when the username was empty, while the sender spoke gomail and always
// authenticated. A relay that needs no AUTH therefore tested clean and failed
// every send, and a server offering only AUTH LOGIN failed BOTH — but only one
// of them where an operator could see it.
//
// ctx cancels the dial and everything after it (DialWithContext), which matters
// because this runs on an HTTP request: a caller who has disconnected should not
// leave us holding a conversation with a stranger's server for the full timeout.
func (t *NetTester) TestSMTP(ctx context.Context, cfg SMTPConfig) error {
	addr, err := vetAddr(ctx, cfg.Host, cfg.Port, allowedSMTPPorts, t.AllowPrivate)
	if err != nil {
		return err
	}

	// The connect-test is a control-plane dial (cmd/inroad), not a worker egress
	// dial, so it binds no source address.
	dialer := &net.Dialer{Timeout: t.Timeout}
	smtpClient, err := newSMTPClient(cfg, t.Timeout, smtpHELO(cfg, ""), smtpDialFunc(addr, dialer))
	if err != nil {
		return err
	}
	if err := smtpClient.DialWithContext(ctx); err != nil {
		return fmt.Errorf("smtp connect: %w", err)
	}
	if err := smtpClient.Close(); err != nil {
		return fmt.Errorf("smtp quit: %w", err)
	}
	return nil
}

// TestIMAP dials the IMAP server, negotiates TLS, and logs in, then logs out.
// Port 143 upgrades via STARTTLS; other ports use implicit TLS.
func (t *NetTester) TestIMAP(ctx context.Context, cfg IMAPConfig) error {
	addr, err := vetAddr(ctx, cfg.Host, cfg.Port, allowedIMAPPorts, t.AllowPrivate)
	if err != nil {
		return err
	}

	// The connect-test is a control-plane dial (cmd/inroad), not a worker egress
	// dial, so it binds no source address.
	c, err := dialIMAP(ctx, addr, cfg, t.Timeout, nil)
	if err != nil {
		return err
	}
	defer func() { _ = c.Logout() }()

	return authenticateIMAP(c, cfg)
}

// dialIMAP connects to addr (an already-vetted "ip:port" string — see vetAddr)
// and negotiates TLS: port 143 dials plaintext then upgrades via STARTTLS;
// other ports (993) dial with implicit TLS. cfg.Host is kept as the TLS
// ServerName even though addr is the resolved IP, so certificate validation
// still checks against the hostname the caller asked for. Shared by TestIMAP
// and NetInboxReader.Fetch so both go through one SSRF-guarded dial path.
//
// ctx cancels the DIAL. It does not cancel later commands: go-imap's Client has
// no context-aware API, so once connected it is the per-response deadline below
// that bounds SELECT/FETCH/LOGIN. That is the honest division — connecting is the
// part that hangs on an unreachable or black-holed host, and it is the part a
// worker shutdown or an abandoned HTTP request needs back.
//
// Dialed by hand rather than through client.DialWithDialer / DialWithDialerTLS,
// which take a *net.Dialer and no context: they cannot be cancelled at all. This
// is the same dial they perform (net or tls, then client.New reads the greeting),
// with DialContext in place of Dial.
//
// timeout is the PER-RESPONSE bound on everything after the dial — the greeting,
// STARTTLS, LOGIN/AUTHENTICATE, SELECT, FETCH — via newDeadlineConn, plus
// go-imap's own per-command Client.Timeout. A timeout <= 0 falls back to
// defaultIMAPTimeout.
//
// The wrapper is not redundant with Client.Timeout, and the gap it closes was
// real: client.New READS THE GREETING, and Client.Timeout cannot be set until New
// returns. go-imap's own DialWithDialer works around that by putting the dialer's
// timeout on the conn as a one-shot deadline before calling New; hand-rolling the
// dial to get a context dropped that workaround, so a server that completed the
// TCP handshake and then said nothing held the caller until it hung up —
// TestIMAPServerThatAcceptsThenGoesSilentIsBounded waited 30 seconds against a
// 750ms timeout. Re-arming per read/write also covers the STARTTLS handshake,
// which neither mechanism bounded.
//
// localAddr optionally binds the SOURCE address of the dial (the worker egress
// IP, spec §15); nil uses the OS default route. addr is the already-vetted
// DESTINATION, so a source bind never affects the SSRF vet (spec §17.7).
func dialIMAP(ctx context.Context, addr string, cfg IMAPConfig, timeout time.Duration, localAddr *net.TCPAddr) (*client.Client, error) {
	if timeout <= 0 {
		timeout = defaultIMAPTimeout
	}
	dialer := &net.Dialer{Timeout: timeout}
	if localAddr != nil {
		dialer.LocalAddr = localAddr
	}

	conn, err := dialIMAPTransport(ctx, addr, cfg, dialer)
	if err != nil {
		return nil, fmt.Errorf("imap dial: %w", err)
	}
	conn = newDeadlineConn(conn, timeout, defaultIMAPTimeout)

	c, err := client.New(conn)
	if err != nil {
		// New reads the server greeting, so it can fail on a connection that opened
		// fine. Closing here rather than leaking the socket.
		_ = conn.Close()
		return nil, fmt.Errorf("imap dial: %w", err)
	}
	c.Timeout = timeout

	if cfg.Port == 143 {
		if err := c.StartTLS(&tls.Config{ServerName: cfg.Host}); err != nil {
			_ = c.Logout()
			return nil, fmt.Errorf("imap dial: %w", err)
		}
	}
	return c, nil
}
