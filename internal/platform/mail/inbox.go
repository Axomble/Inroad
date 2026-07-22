package mail

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	netmail "net/mail"
	"sort"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
)

// NetInboxReader is the production InboxReader. It applies the same SSRF
// protection as NetTester.TestIMAP (see vetAddr) and shares its dial path via
// dialIMAP.
type NetInboxReader struct {
	Timeout      time.Duration
	AllowPrivate bool
}

// NewNetInboxReader returns a NetInboxReader with a sane default dial
// timeout. allowPrivate mirrors NewNetTester's flag.
func NewNetInboxReader(allowPrivate bool) *NetInboxReader {
	return &NetInboxReader{Timeout: 15 * time.Second, AllowPrivate: allowPrivate}
}

// selectInboxReadOnly dials (SSRF-vetted), logs in, and SELECTs INBOX
// read-only, returning the open client (caller must Logout) and its mailbox
// status. Shared by Fetch and CurrentState so both go through one vetted
// dial path.
func (r *NetInboxReader) selectInboxReadOnly(cfg IMAPConfig) (*client.Client, *imap.MailboxStatus, error) {
	addr, err := vetAddr(cfg.Host, cfg.Port, allowedIMAPPorts, r.AllowPrivate)
	if err != nil {
		return nil, nil, err
	}

	c, err := dialIMAP(addr, cfg, r.Timeout)
	if err != nil {
		return nil, nil, err
	}

	if err := c.Login(cfg.Username, cfg.Password); err != nil {
		_ = c.Logout()
		return nil, nil, fmt.Errorf("imap login: %w", err)
	}

	mbox, err := c.Select("INBOX", true) // read-only
	if err != nil {
		_ = c.Logout()
		return nil, nil, fmt.Errorf("imap select: %w", err)
	}
	return c, mbox, nil
}

// CurrentState returns INBOX's current UIDVALIDITY and UIDNEXT — a
// read-only SELECT that fetches no message bodies. Used by the poll handler
// to detect a UIDVALIDITY reset and to establish a first-poll baseline
// without crawling the mailbox's pre-existing history.
func (r *NetInboxReader) CurrentState(cfg IMAPConfig) (uidValidity, uidNext uint32, err error) {
	c, mbox, err := r.selectInboxReadOnly(cfg)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = c.Logout() }()
	return mbox.UidValidity, mbox.UidNext, nil
}

// Fetch logs into cfg's INBOX read-only and returns messages with
// UID > sinceUID, capped at maxN, plus the mailbox's UIDVALIDITY. maxN must
// be positive: it bounds the IMAP UID range requested from the server (see
// uidRangeSeqSet), not just the returned slice, so a first poll (sinceUID==0)
// or a compromised/misbehaving server can never make Fetch pull an unbounded
// number of messages over the wire.
func (r *NetInboxReader) Fetch(cfg IMAPConfig, sinceUID uint32, maxN int) ([]InboundMessage, uint32, error) {
	if maxN <= 0 {
		return nil, 0, fmt.Errorf("mail: Fetch requires maxN > 0, got %d", maxN)
	}

	c, mbox, err := r.selectInboxReadOnly(cfg)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = c.Logout() }()
	uidValidity := mbox.UidValidity

	seqset := uidRangeSeqSet(sinceUID, maxN)

	section := &imap.BodySectionName{} // BODY[] — full message
	items := []imap.FetchItem{imap.FetchUid, section.FetchItem()}

	ch := make(chan *imap.Message, 32)
	done := make(chan error, 1)
	go func() { done <- c.UidFetch(seqset, items, ch) }()

	var out []InboundMessage
	for m := range ch {
		raw := m.GetBody(section)
		if raw == nil {
			// e.g. a concurrent expunge raced the fetch. Log so a silently
			// dropped message is visible instead of just vanishing once the
			// cursor advances past it.
			slog.Warn("mail: fetched message had no body, skipping", "host", cfg.Host, "uid", m.Uid)
			continue
		}
		body, _ := io.ReadAll(raw)
		msg, _ := netmail.ReadMessage(bytes.NewReader(body)) // header parse; tolerate error
		var header netmail.Header
		var postHeaderBody []byte
		if msg != nil {
			header = msg.Header
			postHeaderBody, _ = io.ReadAll(msg.Body)
		}
		out = append(out, InboundMessage{
			UID:         m.Uid,
			Header:      header,
			ContentType: header.Get("Content-Type"),
			Body:        postHeaderBody,
		})
	}
	if err := <-done; err != nil {
		return nil, uidValidity, fmt.Errorf("imap fetch: %w", err)
	}

	return capToLowestUIDs(out, maxN), uidValidity, nil
}

// uidRangeSeqSet builds the IMAP UID range (sinceUID+1):(sinceUID+maxN) — an
// explicit upper bound, never "*". This is the protocol-level guard against
// an unbounded fetch: at most maxN UID-numbers (⇒ at most maxN messages) are
// ever requested from the server, regardless of how large the backlog above
// sinceUID is. capToLowestUIDs is a second, belt-and-braces bound applied to
// the parsed results.
func uidRangeSeqSet(sinceUID uint32, maxN int) *imap.SeqSet {
	seqset := new(imap.SeqSet)
	seqset.AddRange(sinceUID+1, sinceUID+uint32(maxN))
	return seqset
}

// capToLowestUIDs sorts msgs ascending by UID and truncates to the lowest n
// (n <= 0 or already within bound leaves msgs untouched beyond sorting). This
// guarantees the cap never skips mail: whatever falls past the cap has UIDs
// higher than the returned batch, so it's picked up on the next poll once the
// cursor advances past the returned batch's max UID.
func capToLowestUIDs(msgs []InboundMessage, n int) []InboundMessage {
	sort.Slice(msgs, func(i, j int) bool { return msgs[i].UID < msgs[j].UID })
	if n > 0 && len(msgs) > n {
		msgs = msgs[:n]
	}
	return msgs
}
