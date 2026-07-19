package mail

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strconv"
	"time"

	"github.com/emersion/go-imap/client"
)

// NetTester is the production ConnectionTester that dials real SMTP/IMAP servers.
type NetTester struct {
	Timeout time.Duration
}

// NewNetTester returns a NetTester with a sane default dial timeout.
func NewNetTester() *NetTester { return &NetTester{Timeout: 15 * time.Second} }

// TestSMTP dials the SMTP server, negotiates TLS, and authenticates — without
// sending any mail. Port 465 uses implicit TLS; other ports use STARTTLS when
// UseTLS is set.
func (t *NetTester) TestSMTP(cfg SMTPConfig) error {
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))

	var c *smtp.Client
	var err error
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
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))

	var c *client.Client
	var err error
	if cfg.Port == 143 {
		if c, err = client.Dial(addr); err == nil {
			err = c.StartTLS(&tls.Config{ServerName: cfg.Host})
		}
	} else {
		c, err = client.DialTLS(addr, &tls.Config{ServerName: cfg.Host})
	}
	if err != nil {
		return fmt.Errorf("imap dial: %w", err)
	}
	defer func() { _ = c.Logout() }()

	if err := c.Login(cfg.Username, cfg.Password); err != nil {
		return fmt.Errorf("imap login: %w", err)
	}
	return nil
}
