package mail

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
)

// inboxFolder is the fixed destination a rescued warmup message is moved into and
// the folder mark-read operates on. It is a constant, never interpolated from
// caller input.
const inboxFolder = "INBOX"

// NetEngager is the production IMAP Engager. It dials through the SAME SSRF-vetted,
// source-IP-bound path as NetInboxReader (vetAddr + dialIMAP with LocalAddr) — no
// new raw dialer — then locates the message by its RFC822 Message-ID and either
// marks it \Seen (MarkRead) or moves it from its junk folder to INBOX (Rescue).
type NetEngager struct {
	Timeout      time.Duration
	AllowPrivate bool
	// LocalAddr optionally binds the SOURCE address of every IMAP dial to the
	// worker's egress IP (spec §15), so engagement egresses from the same IP as the
	// mailbox's sends. nil = OS default route. Source-only: applied to the
	// net.Dialer after vetAddr vets the destination (spec §17.7).
	LocalAddr *net.TCPAddr
}

// NewNetEngager returns a NetEngager with a sane default dial timeout.
// allowPrivate mirrors NewNetInboxReader's flag.
func NewNetEngager(allowPrivate bool) *NetEngager {
	return &NetEngager{Timeout: 15 * time.Second, AllowPrivate: allowPrivate}
}

// searchByMessageID builds the IMAP SEARCH criteria matching a single RFC822
// Message-ID (SEARCH HEADER Message-Id <id>).
//
// SECURITY (IMAP command injection): the id is attacker-influenceable inbound
// content, so it is placed in the criteria's Header field, which go-imap
// serializes as a properly-quoted/literal protocol argument. It is NEVER
// string-concatenated into a raw IMAP command. This is a pure function so the
// injection-safe construction is unit-testable without a live server.
func searchByMessageID(messageID string) *imap.SearchCriteria {
	c := imap.NewSearchCriteria()
	c.Header.Add("Message-Id", messageID)
	return c
}

// imapCfg builds the IMAPConfig for the recipient's mailbox from an EngageTarget.
func imapCfg(t EngageTarget) IMAPConfig {
	return IMAPConfig{
		Host:     t.IMAPHost,
		Port:     t.IMAPPort,
		Username: t.IMAPUsername,
		Password: string(t.IMAPPassword),
	}
}

// withFolder dials (SSRF-vetted, source-bound), logs in, SELECTs folder
// read-write, and runs fn. The folder name is passed to Select verbatim; go-imap
// encodes a mailbox name as a quoted-string/literal, so an attacker-influenceable
// SourceFolder can never break out into a raw command. The client is always logged
// out. Shared by MarkRead and Rescue so both go through one vetted dial path.
func (e *NetEngager) withFolder(cfg IMAPConfig, folder string, fn func(c *client.Client) error) error {
	addr, err := vetAddr(cfg.Host, cfg.Port, allowedIMAPPorts, e.AllowPrivate)
	if err != nil {
		return err
	}
	c, err := dialIMAP(addr, cfg, e.Timeout, e.LocalAddr)
	if err != nil {
		return err
	}
	defer func() { _ = c.Logout() }()

	if err := c.Login(cfg.Username, cfg.Password); err != nil {
		return fmt.Errorf("imap login: %w", err)
	}
	if _, err := c.Select(folder, false); err != nil { // read-write: STORE/MOVE need it
		return fmt.Errorf("imap select %q: %w", folder, err)
	}
	return fn(c)
}

// findUIDs runs the injection-safe Message-ID search in the currently-selected
// folder and returns the matching UIDs (empty when the message is not there — a
// clean no-op for the caller, e.g. a message already rescued out of the folder).
func findUIDs(c *client.Client, messageID string) (*imap.SeqSet, bool, error) {
	uids, err := c.UidSearch(searchByMessageID(messageID))
	if err != nil {
		return nil, false, fmt.Errorf("imap search: %w", err)
	}
	if len(uids) == 0 {
		return nil, false, nil
	}
	set := new(imap.SeqSet)
	set.AddNum(uids...)
	return set, true, nil
}

// MarkRead sets the \Seen flag on the message in the INBOX. A real recipient reads
// mail in their inbox, and a rescued message has already been moved there, so
// mark-read always targets INBOX; a message that is not there (e.g. a rare 'other'
// placement) yields no search hit and is a clean no-op.
func (e *NetEngager) MarkRead(_ context.Context, t EngageTarget) error {
	return e.withFolder(imapCfg(t), inboxFolder, func(c *client.Client) error {
		set, ok, err := findUIDs(c, t.MessageID)
		if err != nil || !ok {
			return err
		}
		item := imap.FormatFlagsOp(imap.AddFlags, true) // +FLAGS (silent)
		if err := c.UidStore(set, item, []interface{}{imap.SeenFlag}, nil); err != nil {
			return fmt.Errorf("imap store seen: %w", err)
		}
		return nil
	})
}

// Rescue moves the message out of its junk folder into the INBOX ("not spam"),
// using UidMove — which go-imap performs as an RFC 6851 MOVE, falling back to
// COPY + STORE \Deleted + EXPUNGE on servers without the MOVE extension. A message
// already in the inbox (SourceFolder empty or INBOX) needs no rescue.
func (e *NetEngager) Rescue(_ context.Context, t EngageTarget) error {
	if t.SourceFolder == "" || strings.EqualFold(t.SourceFolder, inboxFolder) {
		return nil // already in the inbox — nothing to rescue
	}
	return e.withFolder(imapCfg(t), t.SourceFolder, func(c *client.Client) error {
		set, ok, err := findUIDs(c, t.MessageID)
		if err != nil || !ok {
			return err
		}
		if err := c.UidMove(set, inboxFolder); err != nil {
			return fmt.Errorf("imap move to inbox: %w", err)
		}
		return nil
	})
}
