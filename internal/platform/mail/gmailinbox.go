package mail

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	netmail "net/mail"
	"strconv"
	"strings"

	"golang.org/x/oauth2"
	"golang.org/x/sync/errgroup"
	gmail "google.golang.org/api/gmail/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

// gmailGetConcurrency bounds concurrent users.messages.get calls in one Fetch
// pass: reply/bounce detection is latency-tolerant, so a small fan-out keeps a
// backlog fast without hammering the API or the goroutine budget.
const gmailGetConcurrency = 8

// errGmailHistoryExpired is the internal signal that users.history.list rejected
// the cursor with 404 — Gmail only retains history for ~1 week, so a mailbox
// polled after a long gap has an aged-out startHistoryId. Fetch catches it and
// re-baselines (parallel to the IMAP UIDVALIDITY-reset re-baseline) rather than
// wedging the mailbox on a cursor that can never succeed.
var errGmailHistoryExpired = errors.New("gmail: history id expired")

// GmailReader polls a Gmail mailbox for reply/bounce detection via the Gmail
// API. Gmail has no IMAP UID/UIDVALIDITY; it exposes a monotonic, opaque
// historyId cursor instead. No SSRF vetting: the host is Google's fixed API
// endpoint, not user input.
//
// The unexported func fields are the wire seam (mirroring GmailSender.transmitFn):
// nil selects the real Gmail client call, tests stub them to run network-free.
// A single *gmail.Service is built per Fetch pass (the access token is constant
// within a pass) and threaded through every call.
type GmailReader struct {
	// newServiceFn builds the per-pass Gmail service. nil = the real static-token
	// service (gmailService).
	newServiceFn func(ctx context.Context, accessToken string) (*gmail.Service, error)
	// profileFn reads the mailbox's current historyId (users.getProfile), used as
	// the baseline cursor on a first poll or a history-expired re-baseline.
	profileFn func(ctx context.Context, srv *gmail.Service) (historyID string, err error)
	// historyFn lists messageAdded ids since a cursor and the resume cursor
	// (users.history.list); returns errGmailHistoryExpired on a 404.
	historyFn func(ctx context.Context, srv *gmail.Service, startHistoryID string, maxN int) (msgIDs []string, newCursor string, err error)
	// getFn fetches one message and returns its decoded RFC822 bytes
	// (users.messages.get, format=RAW, base64url-decoded) plus the message's
	// labelIds — the ONLY place a Gmail tab is observable.
	getFn func(ctx context.Context, srv *gmail.Service, msgID string) (raw []byte, labels []string, err error)
	// spamListFn lists the ids of the most-recent messages carrying the SPAM
	// label (users.messages.list, labelIds=SPAM), for warmup spam-placement
	// detection. nil = the real call (gmailSpamList).
	spamListFn func(ctx context.Context, srv *gmail.Service, maxN int) (msgIDs []string, err error)
}

// NewGmailReader returns a GmailReader that talks to the real Gmail API.
func NewGmailReader() *GmailReader { return &GmailReader{} }

// Fetch returns new inbound messages for reply/bounce detection plus the new
// opaque cursor (Gmail historyId). maxN must be positive.
//
// First poll (sinceHistoryID==""): ONLY baseline the cursor to the profile's
// current historyId and return no messages — mirroring NetInboxReader's
// first-poll baseline, which never treats a mailbox's pre-connect inbox as a
// flood of replies/bounces (a mailbox can't have sent anything before it was
// connected, so pre-connect mail can't be a legitimate reply/bounce to it).
//
// Incremental: collect messageAdded ids since the cursor (bounded, resuming
// mid-window if the batch exceeds maxN — see collectHistory), fetch each RAW
// concurrently, and advance the cursor only as far as consumed. A 404 (aged-out
// cursor) re-baselines to the current historyId, dropping one poll window rather
// than wedging the mailbox forever.
//
// The RAW bytes are parsed into an InboundMessage exactly as NetInboxReader.Fetch
// does (netmail.ReadMessage → Header + post-header Body), so the shared ParseDSN
// bounce parser and reply matcher operate on Gmail mail unchanged.
func (g *GmailReader) Fetch(ctx context.Context, accessToken, sinceHistoryID string, maxN int) ([]InboundMessage, string, error) {
	if maxN <= 0 {
		return nil, "", fmt.Errorf("mail: GmailReader.Fetch requires maxN > 0, got %d", maxN)
	}

	srv, err := g.newService(ctx, accessToken)
	if err != nil {
		return nil, "", err
	}

	if sinceHistoryID == "" {
		newCursor, err := g.profile(ctx, srv)
		return nil, newCursor, err
	}

	msgIDs, newCursor, err := g.history(ctx, srv, sinceHistoryID, maxN)
	if errors.Is(err, errGmailHistoryExpired) {
		// Aged-out cursor: re-baseline to the current top and process nothing this
		// pass. One window of replies/bounces is missed (bounded, observable) — the
		// alternative is a cursor that fails every retry forever.
		slog.Warn("gmail history expired, re-baselined", "cursor", sinceHistoryID)
		fresh, perr := g.profile(ctx, srv)
		return nil, fresh, perr
	}
	if err != nil {
		return nil, "", err
	}

	out := make([]InboundMessage, len(msgIDs))
	grp, gctx := errgroup.WithContext(ctx)
	grp.SetLimit(gmailGetConcurrency)
	for i, id := range msgIDs {
		grp.Go(func() error {
			raw, labels, err := g.get(gctx, srv, id)
			if err != nil {
				return err
			}
			// Indexed write: processMessage is order-independent, but a distinct
			// slot per goroutine avoids a data race without a mutex.
			out[i] = parseInbound(raw, labels)
			return nil
		})
	}
	if err := grp.Wait(); err != nil {
		// Cursor stays unadvanced (not returned): the whole window retries next pass.
		return nil, "", err
	}
	return out, newCursor, nil
}

// collectHistory accumulates messageAdded ids from one history.list page and
// computes the resume cursor. When the window is fully drained (every record
// consumed, no further page) the cursor advances to respHistoryID (the mailbox's
// current top) so an empty or fully-consumed window still advances and is never
// re-scanned. When collection stops short — maxN reached with records still
// pending, or the page was paginated (nextPageToken) and we did not follow it —
// the cursor is pinned to the LAST CONSUMED record's Id, so the next poll resumes
// mid-window and the untaken remainder is picked up rather than skipped. Slight
// boundary re-processing on resume is harmless: MarkReplied/MarkBounced/
// suppression are idempotent.
func collectHistory(records []*gmail.History, respHistoryID uint64, nextPageToken string, maxN int) ([]string, string) {
	var ids []string
	var lastConsumedID uint64
	truncated := false
	for i, h := range records {
		for _, added := range h.MessagesAdded {
			if added.Message != nil {
				ids = append(ids, added.Message.Id)
			}
		}
		lastConsumedID = h.Id
		// Stop only on a record boundary (a record's Id is the only checkpoint we
		// can resume from): if this record filled the batch and more remain, hand
		// the rest to the next poll.
		if len(ids) >= maxN && i < len(records)-1 {
			truncated = true
			break
		}
	}
	// Resume from the checkpoint when we stopped short; otherwise the window is
	// drained and the cursor jumps to the mailbox's current top. lastConsumedID==0
	// means we consumed nothing (empty window), so there is no checkpoint to pin.
	if (truncated || nextPageToken != "") && lastConsumedID != 0 {
		return ids, strconv.FormatUint(lastConsumedID, 10)
	}
	return ids, strconv.FormatUint(respHistoryID, 10)
}

// parseInbound turns a raw RFC822 message into an InboundMessage the same way
// NetInboxReader.Fetch does: parse the header, then read the body that follows
// it. A parse failure is tolerated (best-effort empty header/body) so one
// malformed message never fails the whole poll pass. UID is left zero — Gmail
// tracks position by the historyId cursor, not per-message UIDs.
//
// labels are the API's labelIds for this message, which no RFC822 header carries:
// they are how a Gmail tab becomes observable at all.
func parseInbound(raw []byte, labels []string) InboundMessage {
	msg, _ := netmail.ReadMessage(bytes.NewReader(raw))
	var header netmail.Header
	var postHeaderBody []byte
	if msg != nil {
		header = msg.Header
		postHeaderBody, _ = io.ReadAll(msg.Body)
	}
	return InboundMessage{
		Header:            header,
		ContentType:       header.Get("Content-Type"),
		Body:              postHeaderBody,
		PlacementCategory: gmailPlacementCategory(labels),
	}
}

// Gmail's category labels. The four tabs are the observable ones;
// CATEGORY_PERSONAL names the PRIMARY tab, which is not a tab landing at all.
const (
	gmailCategoryPrefix  = "CATEGORY_"
	gmailCategoryPrimary = "PERSONAL"
)

// gmailPlacementCategory reduces a message's labelIds to the TAB it was filed
// under, or "" for the primary inbox — the only positively-observed tab signal any
// provider gives us.
//
// Three rules, each load-bearing:
//
//   - CATEGORY_PERSONAL is the primary tab, so it names NO tab. Gmail applies it to
//     ordinary inbox mail, so treating it as a tab would report a ~100% tabbed rate
//     for precisely the mailboxes whose mail lands where we want it.
//   - An UNRECOGNISED category (Gmail adds one) IS a tab. It is a category; the
//     alternative is silently recording it as the primary inbox, which is the
//     optimistic direction, and optimism is the wrong default for a reputation
//     signal.
//   - SPAM wins over any category, wherever it appears in the list. A spam-foldered
//     message is not in the inbox at all, so no tab applies to it.
//
// The first tab wins so the result does not depend on label order, which the API
// makes no promise about.
func gmailPlacementCategory(labels []string) string {
	tab := ""
	for _, label := range labels {
		if label == gmailLabelSpam {
			return ""
		}
		if tab != "" || !strings.HasPrefix(label, gmailCategoryPrefix) {
			continue
		}
		if name := strings.TrimPrefix(label, gmailCategoryPrefix); name != gmailCategoryPrimary {
			tab = strings.ToLower(name)
		}
	}
	return tab
}

// FetchSpam best-effort returns the most-recent (bounded) messages Gmail routed
// to SPAM, for warmup spam-placement detection. A message Gmail marks as spam
// carries the SPAM label and NOT INBOX, so the incremental INBOX history poll
// (Fetch) never sees it — this separate scan is how spam-placed warmup mail is
// observed. Stateless by design (no cursor): the warmup receipt write is
// idempotent, so re-listing SPAM every poll never double-records. maxN must be
// positive; it bounds the messages.list page and the per-message fetch fan-out.
func (g *GmailReader) FetchSpam(ctx context.Context, accessToken string, maxN int) ([]InboundMessage, error) {
	if maxN <= 0 {
		return nil, fmt.Errorf("mail: GmailReader.FetchSpam requires maxN > 0, got %d", maxN)
	}
	srv, err := g.newService(ctx, accessToken)
	if err != nil {
		return nil, err
	}
	msgIDs, err := g.spamList(ctx, srv, maxN)
	if err != nil {
		return nil, err
	}
	out := make([]InboundMessage, len(msgIDs))
	grp, gctx := errgroup.WithContext(ctx)
	grp.SetLimit(gmailGetConcurrency)
	for i, id := range msgIDs {
		grp.Go(func() error {
			raw, labels, err := g.get(gctx, srv, id)
			if err != nil {
				return err
			}
			out[i] = parseInbound(raw, labels)
			return nil
		})
	}
	if err := grp.Wait(); err != nil {
		return nil, err
	}
	return out, nil
}

func (g *GmailReader) spamList(ctx context.Context, srv *gmail.Service, maxN int) ([]string, error) {
	if g.spamListFn != nil {
		return g.spamListFn(ctx, srv, maxN)
	}
	return gmailSpamList(ctx, srv, maxN)
}

// gmailSpamList lists the ids of up to maxN most-recent SPAM-labelled messages.
// messages.list returns newest-first, so a small MaxResults cap naturally yields
// the recent window a warmup message would be in without paging the whole folder.
func gmailSpamList(ctx context.Context, srv *gmail.Service, maxN int) ([]string, error) {
	resp, err := srv.Users.Messages.List("me").
		LabelIds("SPAM").
		MaxResults(int64(maxN)).
		Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("gmail: messages.list(SPAM): %w", err)
	}
	ids := make([]string, 0, len(resp.Messages))
	for _, m := range resp.Messages {
		if m != nil && m.Id != "" {
			ids = append(ids, m.Id)
		}
	}
	return ids, nil
}

func (g *GmailReader) newService(ctx context.Context, accessToken string) (*gmail.Service, error) {
	if g.newServiceFn != nil {
		return g.newServiceFn(ctx, accessToken)
	}
	return gmailService(ctx, accessToken)
}

func (g *GmailReader) profile(ctx context.Context, srv *gmail.Service) (string, error) {
	if g.profileFn != nil {
		return g.profileFn(ctx, srv)
	}
	return gmailProfileHistoryID(ctx, srv)
}

func (g *GmailReader) history(ctx context.Context, srv *gmail.Service, startHistoryID string, maxN int) ([]string, string, error) {
	if g.historyFn != nil {
		return g.historyFn(ctx, srv, startHistoryID, maxN)
	}
	return gmailHistory(ctx, srv, startHistoryID, maxN)
}

func (g *GmailReader) get(ctx context.Context, srv *gmail.Service, msgID string) ([]byte, []string, error) {
	if g.getFn != nil {
		return g.getFn(ctx, srv, msgID)
	}
	return gmailGetRAW(ctx, srv, msgID)
}

// gmailService builds a Gmail API service bound to a static access token (no
// refresh — the fresh token is minted upstream in coreapi). Built once per Fetch
// pass and reused across the history call and every message get.
func gmailService(ctx context.Context, accessToken string) (*gmail.Service, error) {
	client := oauth2.NewClient(ctx, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: accessToken}))
	srv, err := gmail.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return nil, fmt.Errorf("gmail: service: %w", err)
	}
	return srv, nil
}

// gmailProfileHistoryID reads the mailbox's current historyId to baseline the
// cursor on a first poll or a history-expired re-baseline.
func gmailProfileHistoryID(ctx context.Context, srv *gmail.Service) (string, error) {
	p, err := srv.Users.GetProfile("me").Context(ctx).Do()
	if err != nil {
		return "", fmt.Errorf("gmail: getProfile: %w", err)
	}
	return strconv.FormatUint(p.HistoryId, 10), nil
}

// gmailHistory lists messageAdded ids in INBOX since startHistoryID and computes
// the resume cursor (collectHistory). LabelId("INBOX") mirrors the IMAP reader,
// which polls INBOX only — so we never waste a messages.get on the mailbox's own
// Sent/Drafts. A 404 (aged-out cursor) is surfaced as errGmailHistoryExpired so
// Fetch can re-baseline.
func gmailHistory(ctx context.Context, srv *gmail.Service, startHistoryID string, maxN int) ([]string, string, error) {
	start, err := strconv.ParseUint(startHistoryID, 10, 64)
	if err != nil {
		return nil, "", fmt.Errorf("gmail: parse historyId %q: %w", startHistoryID, err)
	}
	resp, err := srv.Users.History.List("me").
		StartHistoryId(start).
		HistoryTypes("messageAdded").
		LabelId("INBOX").
		MaxResults(int64(maxN)).
		Context(ctx).Do()
	if err != nil {
		var gerr *googleapi.Error
		if errors.As(err, &gerr) && gerr.Code == 404 {
			return nil, "", errGmailHistoryExpired
		}
		return nil, "", fmt.Errorf("gmail: history.list: %w", err)
	}
	ids, newCursor := collectHistory(resp.History, resp.HistoryId, resp.NextPageToken, maxN)
	return ids, newCursor, nil
}

// gmailGetRAW fetches one message as RFC822 (format=RAW) and base64url-decodes
// it to the same bytes NetInboxReader hands to netmail.ReadMessage, along with the
// message's labelIds.
//
// The labels cost nothing extra: format=RAW already returns them on the same
// Message this call was making anyway, and they were previously dropped one line
// before use — which is why every Gmail Promotions landing was recorded as a
// primary-inbox landing.
func gmailGetRAW(ctx context.Context, srv *gmail.Service, msgID string) ([]byte, []string, error) {
	m, err := srv.Users.Messages.Get("me", msgID).Format("RAW").Context(ctx).Do()
	if err != nil {
		return nil, nil, fmt.Errorf("gmail: messages.get: %w", err)
	}
	return gmailMessageRAW(m)
}

// gmailMessageRAW is everything gmailGetRAW does apart from the network call: the
// base64url decode and the labels. Split out so the part that can be wrong is
// testable — the call above is stubbed away in every test, and "return the labels
// the API gave us" is exactly the line that was missing.
func gmailMessageRAW(m *gmail.Message) ([]byte, []string, error) {
	raw, err := base64.URLEncoding.DecodeString(m.Raw)
	if err != nil {
		return nil, nil, fmt.Errorf("gmail: decode raw: %w", err)
	}
	return raw, m.LabelIds, nil
}
