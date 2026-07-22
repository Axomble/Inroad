// Package mail provides SMTP/IMAP connectivity checks used when connecting a
// mailbox, and (later) sending and polling. It is the only place raw SMTP/IMAP
// clients are imported for per-user (caller-supplied) mailbox hosts; the
// operator's system mailbox for transactional email is handled separately by
// platform/notify.
package mail

import (
	netmail "net/mail"
)

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

// InboundMessage is a single message fetched from a mailbox's INBOX, parsed
// enough for reply/bounce detection (Task 2's DSN parser) to consume.
type InboundMessage struct {
	UID         uint32
	Header      netmail.Header // parsed RFC5322 header (From, In-Reply-To, References, Auto-Submitted, Content-Type, Message-ID)
	ContentType string
	Body        []byte // message body AFTER the top-level headers (not the raw message)
}

// InboxReader polls a mailbox for new messages. Domains depend on this
// interface, not the concrete IMAP client, so they can be unit-tested with a
// fake.
type InboxReader interface {
	// Fetch returns messages with UID > sinceUID from INBOX (capped at maxN,
	// which must be positive — it bounds the IMAP request itself, not just
	// the returned slice), plus the mailbox's current UIDVALIDITY. Reusing
	// IMAPConfig keeps mailbox credential wiring identical to
	// ConnectionTester.TestIMAP.
	Fetch(cfg IMAPConfig, sinceUID uint32, maxN int) (msgs []InboundMessage, uidValidity uint32, err error)
	// CurrentState reports INBOX's current UIDVALIDITY and UIDNEXT via a
	// read-only SELECT, without fetching any message bodies. The poll handler
	// uses it to detect a UIDVALIDITY reset and to establish a first-poll
	// baseline (see Fetch's callers) without pulling any mail over the wire.
	CurrentState(cfg IMAPConfig) (uidValidity, uidNext uint32, err error)
}
