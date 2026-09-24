package mail

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
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

// TestSMTP dials the SMTP server, negotiates TLS, and authenticates — without
// sending any mail. TLS is enforced by default (security Invariant 6): port 465
// uses implicit TLS, every other port requires STARTTLS — cleartext auth is
// permitted ONLY when cfg.AllowPlaintext is explicitly set.
func (t *NetTester) TestSMTP(ctx context.Context, cfg SMTPConfig) error {
	addr, err := vetAddr(ctx, cfg.Host, cfg.Port, allowedSMTPPorts, t.AllowPrivate)
	if err != nil {
		return err
	}

	// DialContext rather than a bare timeout: this runs on an HTTP request, and a
	// caller who has disconnected should not leave us holding a half-open dial to a
	// stranger's server for the full t.Timeout. The timeout stays as the ceiling for
	// a caller who is still waiting.
	dialer := &net.Dialer{Timeout: t.Timeout}
	conn, derr := dialSMTPTransport(ctx, addr, dialer)
	if derr != nil {
		return fmt.Errorf("smtp dial: %w", derr)
	}
	if cfg.Port == 465 {
		conn, derr = smtpImplicitTLS(ctx, conn, cfg.Host)
		if derr != nil {
			return fmt.Errorf("smtp dial: %w", derr)
		}
	}
	// Every byte after the dial gets a deadline we chose, refreshed per read and
	// per write (see newDeadlineConn). net/smtp has no context-aware API and no
	// per-command timeout of its own, so without this a server that completes the
	// TCP handshake and then stalls on the greeting, EHLO, STARTTLS or AUTH held
	// an API request — this runs on one — until the client gave up. The dial
	// timeout never covered any of that.
	conn = newDeadlineConn(conn, t.Timeout, defaultSMTPTimeout)

	c, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		// NewClient reads the server greeting, so it can fail on a connection that
		// opened fine. Closing here rather than leaking the socket until GC.
		_ = conn.Close()
		return fmt.Errorf("smtp client: %w", err)
	}
	defer c.Close()

	if cfg.Port != 465 && !cfg.AllowPlaintext {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(&tls.Config{ServerName: cfg.Host}); err != nil {
				return fmt.Errorf("smtp starttls: %w", err)
			}
		} else {
			return fmt.Errorf("smtp server does not advertise STARTTLS but TLS is required (set allow_plaintext to override)")
		}
	}

	if cfg.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	return c.Quit()
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
