package mail

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	netmail "net/mail"
	"strings"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
)

// ErrNoJunkFolder signals that a provider's spam/junk folder could not be
// resolved for this mailbox. It is NOT a poll failure: the warmup receipt-
// detection hook logs it and proceeds with INBOX-only placement detection (spec
// §7). Callers test it with errors.Is.
var ErrNoJunkFolder = errors.New("mail: no junk/spam folder resolved")

// junkFolderCandidates are the common junk/spam mailbox names tried, in order,
// when a server does NOT advertise the RFC 6154 SPECIAL-USE \Junk attribute.
// Matching is case-insensitive (junkFolderMatches). This is deliberately
// best-effort: IMAP has no mandated junk-folder name, so a server using an
// exotic name yields ErrNoJunkFolder and warmup placement detection falls back
// to INBOX-only rather than failing the poll.
var junkFolderCandidates = []string{
	"Junk",
	"Spam",
	"Junk E-mail",
	"Junk Email",
	"[Gmail]/Spam",
	"Bulk Mail",
}

// pickJunkFolder chooses the mailbox to scan for spam-placed warmup mail from a
// server's LIST output. It prefers the RFC 6154 SPECIAL-USE \Junk folder (the
// authoritative signal, whatever the server named it); failing that it falls
// back to the first case-insensitive match against junkFolderCandidates. It is a
// pure function of the LIST result so the resolution logic is unit-testable
// without a live IMAP server. Returns ("", false) when nothing matches.
func pickJunkFolder(infos []*imap.MailboxInfo) (string, bool) {
	// A \Noselect placeholder can never be SELECTed, so it is not a scan target
	// even if it carries the \Junk attribute or a candidate name.
	selectable := func(info *imap.MailboxInfo) bool {
		for _, a := range info.Attributes {
			if a == imap.NoSelectAttr {
				return false
			}
		}
		return true
	}
	for _, info := range infos {
		if info == nil || !selectable(info) {
			continue
		}
		for _, a := range info.Attributes {
			if a == imap.JunkAttr {
				return info.Name, true
			}
		}
	}
	for _, cand := range junkFolderCandidates {
		for _, info := range infos {
			if info == nil || !selectable(info) {
				continue
			}
			if strings.EqualFold(info.Name, cand) {
				return info.Name, true
			}
		}
	}
	return "", false
}

// FetchJunk best-effort scans the mailbox's spam/junk folder for warmup mail and
// returns the most-recent (bounded) messages plus the resolved folder name. It
// dials through the SAME SSRF-vetted path as Fetch/CurrentState, LISTs the
// mailbox tree, resolves the junk folder (pickJunkFolder), SELECTs it read-only,
// and pulls at most maxN of its highest-UID messages. It returns ErrNoJunkFolder
// (a non-fatal signal, not a poll failure) when no junk folder can be resolved.
// Stateless by design: there is no junk cursor — the warmup receipt write is
// idempotent (UNIQUE on (send, recipient)), so re-scanning the same messages
// every poll never double-records.
func (r *NetInboxReader) FetchJunk(ctx context.Context, cfg IMAPConfig, maxN int) ([]InboundMessage, string, error) {
	if maxN <= 0 {
		return nil, "", fmt.Errorf("mail: FetchJunk requires maxN > 0, got %d", maxN)
	}

	addr, err := vetAddr(cfg.Host, cfg.Port, allowedIMAPPorts, r.AllowPrivate)
	if err != nil {
		return nil, "", err
	}
	c, err := dialIMAP(ctx, addr, cfg, r.Timeout, r.LocalAddr)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = c.Logout() }()
	if err := c.Login(cfg.Username, cfg.Password); err != nil {
		return nil, "", fmt.Errorf("imap login: %w", err)
	}

	folder, ok, err := r.resolveJunkFolder(c)
	if err != nil {
		return nil, "", err
	}
	if !ok {
		return nil, "", ErrNoJunkFolder
	}

	mbox, err := c.Select(folder, true) // read-only
	if err != nil {
		return nil, "", fmt.Errorf("imap select junk: %w", err)
	}

	msgs, err := fetchRecentUIDs(c, mbox.UidNext, maxN)
	if err != nil {
		return nil, "", err
	}
	return msgs, folder, nil
}

// resolveJunkFolder LISTs the mailbox tree and applies pickJunkFolder. A LIST
// failure is surfaced (transient server error), distinct from a clean
// "no junk folder exists" (ok=false).
func (r *NetInboxReader) resolveJunkFolder(c *client.Client) (name string, ok bool, err error) {
	ch := make(chan *imap.MailboxInfo, 32)
	done := make(chan error, 1)
	go func() { done <- c.List("", "*", ch) }()

	var infos []*imap.MailboxInfo
	for info := range ch {
		infos = append(infos, info)
	}
	if lerr := <-done; lerr != nil {
		return "", false, fmt.Errorf("imap list: %w", lerr)
	}
	name, ok = pickJunkFolder(infos)
	return name, ok, nil
}

// fetchRecentUIDs pulls at most maxN of a selected folder's highest-UID messages
// — the recent ones a warmup message would be among. The requested UID range is
// bounded to maxN numbers at the protocol level (never "*"), so a huge junk
// folder can never make one scan pull an unbounded number of messages. An empty
// folder (uidNext <= 1) is a clean no-op.
func fetchRecentUIDs(c *client.Client, uidNext uint32, maxN int) ([]InboundMessage, error) {
	if uidNext <= 1 {
		return nil, nil
	}
	top := uidNext - 1
	var from uint32 = 1
	if top > uint32(maxN) {
		from = top - uint32(maxN) + 1
	}
	seqset := new(imap.SeqSet)
	seqset.AddRange(from, top)

	section := &imap.BodySectionName{}
	items := []imap.FetchItem{imap.FetchUid, section.FetchItem()}
	ch := make(chan *imap.Message, 32)
	done := make(chan error, 1)
	go func() { done <- c.UidFetch(seqset, items, ch) }()

	var out []InboundMessage
	for m := range ch {
		raw := m.GetBody(section)
		if raw == nil {
			continue
		}
		body, _ := io.ReadAll(raw)
		msg, _ := netmail.ReadMessage(bytes.NewReader(body))
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
		return nil, fmt.Errorf("imap fetch junk: %w", err)
	}
	return out, nil
}
