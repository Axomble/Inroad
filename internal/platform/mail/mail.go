// Package mail provides SMTP/IMAP connectivity checks used when connecting a
// mailbox, and (later) sending and polling. It is the only place raw SMTP/IMAP
// clients are imported for per-user (caller-supplied) mailbox hosts; the
// operator's system mailbox for transactional email is handled separately by
// platform/notify.
package mail

// SMTPConfig holds the outbound settings for a mailbox.
type SMTPConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	UseTLS   bool
}

// IMAPConfig holds the inbound (reply/bounce polling) settings for a mailbox.
type IMAPConfig struct {
	Host     string
	Port     int
	Username string
	Password string
}

// ConnectionTester verifies mailbox credentials against real servers before we
// persist them (PRD 9.1.3). Domains depend on this interface, not the concrete
// dialer, so they can be unit-tested with a fake.
type ConnectionTester interface {
	TestSMTP(cfg SMTPConfig) error
	TestIMAP(cfg IMAPConfig) error
}
