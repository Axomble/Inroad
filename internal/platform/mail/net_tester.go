package mail

import (
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
// sending any mail. Port 465 uses implicit TLS; other ports use STARTTLS when
// UseTLS is set.
func (t *NetTester) TestSMTP(cfg SMTPConfig) error {
	addr, err := vetAddr(cfg.Host, cfg.Port, allowedSMTPPorts, t.AllowPrivate)
	if err != nil {
		return err
	}

	var c *smtp.Client
	if cfg.Port == 465 {
		conn, derr := tls.DialWithDialer(&net.Dialer{Timeout: t.Timeout}, "tcp", addr, &tls.Config{ServerName: cfg.Host})
		if derr != nil {
			return fmt.Errorf("smtp dial: %w", derr)
		}
		c, err = smtp.NewClient(conn, cfg.Host)
	} else {
		conn, derr := net.DialTimeout("tcp", addr, t.Timeout)
		if derr != nil {
			return fmt.Errorf("smtp dial: %w", derr)
		}
		c, err = smtp.NewClient(conn, cfg.Host)
	}
	if err != nil {
		return fmt.Errorf("smtp client: %w", err)
	}
	defer c.Close()

	if cfg.Port != 465 && cfg.UseTLS {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(&tls.Config{ServerName: cfg.Host}); err != nil {
				return fmt.Errorf("smtp starttls: %w", err)
			}
		} else {
			return fmt.Errorf("smtp server does not advertise STARTTLS but TLS was required")
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
func (t *NetTester) TestIMAP(cfg IMAPConfig) error {
	addr, err := vetAddr(cfg.Host, cfg.Port, allowedIMAPPorts, t.AllowPrivate)
	if err != nil {
		return err
	}

	c, err := dialIMAP(addr, cfg, t.Timeout)
	if err != nil {
		return err
	}
	defer func() { _ = c.Logout() }()

	if err := c.Login(cfg.Username, cfg.Password); err != nil {
		return fmt.Errorf("imap login: %w", err)
	}
	return nil
}

// dialIMAP connects to addr (an already-vetted "ip:port" string — see vetAddr)
// and negotiates TLS: port 143 dials plaintext then upgrades via STARTTLS;
// other ports (993) dial with implicit TLS. cfg.Host is kept as the TLS
// ServerName even though addr is the resolved IP, so certificate validation
// still checks against the hostname the caller asked for. Shared by TestIMAP
// and NetInboxReader.Fetch so both go through one SSRF-guarded dial path.
//
// timeout bounds both the initial dial+greeting (via a net.Dialer deadline)
// and every subsequent IMAP command — STARTTLS, LOGIN, SELECT, FETCH, ... —
// via go-imap's per-command deadline (Client.Timeout), so a hung server can
// never block the caller indefinitely. A timeout <= 0 falls back to
// defaultIMAPTimeout.
func dialIMAP(addr string, cfg IMAPConfig, timeout time.Duration) (*client.Client, error) {
	if timeout <= 0 {
		timeout = defaultIMAPTimeout
	}
	dialer := &net.Dialer{Timeout: timeout}

	var c *client.Client
	var err error
	if cfg.Port == 143 {
		if c, err = client.DialWithDialer(dialer, addr); err == nil {
			err = c.StartTLS(&tls.Config{ServerName: cfg.Host})
		}
	} else {
		c, err = client.DialWithDialerTLS(dialer, addr, &tls.Config{ServerName: cfg.Host})
	}
	if err != nil {
		return nil, fmt.Errorf("imap dial: %w", err)
	}
	c.Timeout = timeout
	return c, nil
}
