package mail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/oauth2"
	"golang.org/x/sync/errgroup"
)

// graphGetConcurrency bounds concurrent /$value gets in one Fetch pass:
// reply/bounce detection is latency-tolerant, so a small fan-out keeps a backlog
// fast without hammering Graph or the goroutine budget. Mirrors gmailGetConcurrency.
const graphGetConcurrency = 8

// graphInboxDeltaURL is the delta query that baselines the cursor:
// $deltatoken=latest returns an empty value plus an @odata.deltaLink pointing at
// the current top of the Inbox, so a first poll (or a re-baseline) processes
// nothing and only stores the checkpoint. The host is Graph's fixed API
// endpoint, not user input, so no SSRF vetting is needed.
const graphInboxDeltaURL = "https://graph.microsoft.com/v1.0/me/mailFolders('inbox')/messages/delta?$deltatoken=latest"

// errGraphDeltaExpired is the internal signal that the stored delta/skip token
// aged out: Graph returns 410 Gone (or a resync-required 400) when a delta token
// is too old to resume from. Fetch catches it and re-baselines (parallel to the
// Gmail 404 re-baseline and the IMAP UIDVALIDITY-reset re-baseline) rather than
// wedging the mailbox on a cursor that can never succeed.
var errGraphDeltaExpired = errors.New("graph: delta token expired")

// deltaMsg is one entry in a Graph messages-delta page. Only the id and the
// presence of an @removed marker matter here: an id feeds the /$value fetch; an
// @removed entry is a deletion/move out of the folder (no inbound mail to
// classify) and is skipped.
type deltaMsg struct {
	ID      string          `json:"id"`
	Removed json.RawMessage `json:"@removed"`
}

// GraphReader polls an M365 mailbox for reply/bounce detection via the Microsoft
// Graph delta query. Graph has no IMAP UID/UIDVALIDITY and no Gmail historyId;
// it exposes an opaque delta/next-link URL cursor instead.
//
// SSRF: the baseline uses the fixed graphInboxDeltaURL constant, but the
// incremental cursor is an OPAQUE URL persisted from a prior Graph response, and
// Fetch dials it with the mailbox's bearer attached. That cursor is therefore
// host-pinned to graph.microsoft.com (graphHostPinned) before it is dialed, so a
// corrupted or hostile stored value can never exfiltrate the access token to
// another host — a mismatch is treated as an expired cursor and re-baselined.
//
// The unexported func fields are the wire seam (mirroring GmailReader's): nil
// selects the real Graph call, tests stub them to run network-free. The access
// token is constant within a pass and threaded through every call.
type GraphReader struct {
	// deltaFn GETs one delta page (the baseline or the stored cursor URL) and
	// returns its message entries, the @odata.nextLink (more pages) and the
	// @odata.deltaLink (final page); returns errGraphDeltaExpired on 410/resync.
	deltaFn func(ctx context.Context, accessToken, url string) (value []deltaMsg, nextLink, deltaLink string, err error)
	// baselineFn reads the current top-of-Inbox deltaLink ($deltatoken=latest),
	// used on a first poll or a delta-expired re-baseline.
	baselineFn func(ctx context.Context, accessToken string) (deltaLink string, err error)
	// getRawFn fetches one message's RAW RFC822 bytes (/$value returns raw MIME
	// directly — NOT base64, so unlike Gmail there is no decode step).
	getRawFn func(ctx context.Context, accessToken, id string) (raw []byte, err error)
}

// NewGraphReader returns a GraphReader that talks to the real Graph API.
func NewGraphReader() *GraphReader { return &GraphReader{} }

// Fetch returns new inbound messages for reply/bounce detection plus the new
// opaque cursor (a Graph delta/next-link URL). maxN must be positive.
//
// First poll (sinceCursor==""): ONLY baseline the cursor to the current
// top-of-Inbox deltaLink and return no messages — mirroring GmailReader's
// first-poll baseline, so a mailbox's pre-connect inbox is never treated as a
// flood of replies/bounces (a mailbox can't have sent anything before it was
// connected, so pre-connect mail can't be a legitimate reply/bounce to it).
//
// Incremental: GET the stored cursor URL, collect changed-message ids (bounded,
// resuming at the nextLink if the page is truncated or more pages exist — see
// collectDelta), fetch each RAW concurrently, and advance the cursor only as far
// as consumed. A 410/resync (aged-out token) re-baselines to the current top,
// dropping one poll window rather than wedging the mailbox forever.
//
// The RAW bytes are parsed into an InboundMessage exactly as the Gmail and IMAP
// readers do (netmail.ReadMessage → Header + post-header Body), so the shared
// ParseDSN bounce parser and reply matcher operate on Graph mail unchanged.
func (g *GraphReader) Fetch(ctx context.Context, accessToken, sinceCursor string, maxN int) ([]InboundMessage, string, error) {
	if maxN <= 0 {
		return nil, "", fmt.Errorf("mail: GraphReader.Fetch requires maxN > 0, got %d", maxN)
	}

	if sinceCursor == "" {
		deltaLink, err := g.baseline(ctx, accessToken)
		return nil, deltaLink, err
	}

	// SSRF guard: the stored cursor is dialed with the mailbox bearer, so it must
	// be pinned to Graph's host before use. A corrupted/hostile value is handled
	// exactly like an expired cursor (re-baseline from the fixed baseline URL) so
	// the bearer is never sent off-host. Log only the length, never the token.
	if !graphHostPinned(sinceCursor) {
		slog.Warn("graph cursor host not pinned, re-baselined", "cursor_len", len(sinceCursor))
		fresh, berr := g.baseline(ctx, accessToken)
		return nil, fresh, berr
	}

	value, nextLink, deltaLink, err := g.delta(ctx, accessToken, sinceCursor)
	if errors.Is(err, errGraphDeltaExpired) {
		// Aged-out token: re-baseline to the current top and process nothing this
		// pass. One window of replies/bounces is missed (bounded, observable) — the
		// alternative is a cursor that fails every retry forever. Log only the
		// length: the delta token is an opaque capability value, never emitted verbatim.
		slog.Warn("graph delta expired, re-baselined", "cursor_len", len(sinceCursor))
		fresh, berr := g.baseline(ctx, accessToken)
		return nil, fresh, berr
	}
	if err != nil {
		return nil, "", err
	}

	ids, newCursor := collectDelta(value, nextLink, deltaLink, maxN)

	out := make([]InboundMessage, len(ids))
	grp, gctx := errgroup.WithContext(ctx)
	grp.SetLimit(graphGetConcurrency)
	for i, id := range ids {
		grp.Go(func() error {
			raw, err := g.getRaw(gctx, accessToken, id)
			if err != nil {
				return err
			}
			// Indexed write: processMessage is order-independent, but a distinct
			// slot per goroutine avoids a data race without a mutex.
			out[i] = parseInbound(raw)
			return nil
		})
	}
	if err := grp.Wait(); err != nil {
		// Cursor stays unadvanced (not returned): the whole window retries next pass.
		return nil, "", err
	}
	return out, newCursor, nil
}

// collectDelta accumulates changed-message ids from one delta page and computes
// the resume cursor. @removed entries (deletions/moves out of the folder) are
// skipped — only new/changed messages can be a reply or bounce.
//
// The maxN bound is HARD only while a further page exists (an @odata.nextLink to
// resume from): once the batch fills and more entries remain in a paged response,
// collection stops and the cursor is the nextLink, so the untaken remainder is
// picked up at the next poll rather than skipped. On the TERMINAL page (no
// nextLink) there is nothing after it to resume from, so the maxN bound goes soft
// — the whole page is consumed (bounded by Graph's own page size) and the cursor
// is the @odata.deltaLink. This is the correctness fix for the drop bug: honoring
// a maxN truncation on a page with no nextLink would leave the unconsumed
// remainder unreachable AND yield an empty ("") cursor, which the next poll would
// misread as a first poll and re-baseline over — silently skipping that mail.
//
// An empty page also advances to the deltaLink, so a fully-consumed or empty
// window still advances and is never re-scanned. collectDelta NEVER returns an
// empty cursor. Slight boundary re-processing on resume is harmless:
// MarkReplied/MarkBounced/suppression are idempotent.
func collectDelta(value []deltaMsg, nextLink, deltaLink string, maxN int) ([]string, string) {
	var ids []string
	truncated := false
	for i, m := range value {
		if m.Removed != nil || m.ID == "" {
			continue
		}
		ids = append(ids, m.ID)
		// Only break early when there is a nextLink to resume from; on the terminal
		// page consume everything (never strand the remainder behind an empty cursor).
		if len(ids) >= maxN && nextLink != "" && i < len(value)-1 {
			truncated = true
			break
		}
	}
	if truncated || nextLink != "" {
		return ids, nextLink
	}
	return ids, deltaLink
}

// graphHostPinned reports whether an opaque stored delta cursor is safe to dial
// with the mailbox bearer: it must be an absolute https URL whose host is exactly
// graph.microsoft.com. Anything else — a plain-http URL, another host, embedded
// userinfo like https://graph.microsoft.com@evil.example (url.Parse puts the real
// host in u.Host), or an unparseable value — is rejected so the access token can
// never be sent off-host.
func graphHostPinned(cursor string) bool {
	u, err := url.Parse(cursor)
	if err != nil {
		return false
	}
	return u.Scheme == "https" && u.Host == "graph.microsoft.com"
}

func (g *GraphReader) delta(ctx context.Context, accessToken, u string) ([]deltaMsg, string, string, error) {
	if g.deltaFn != nil {
		return g.deltaFn(ctx, accessToken, u)
	}
	return graphDelta(ctx, accessToken, u)
}

func (g *GraphReader) baseline(ctx context.Context, accessToken string) (string, error) {
	if g.baselineFn != nil {
		return g.baselineFn(ctx, accessToken)
	}
	return graphBaseline(ctx, accessToken)
}

func (g *GraphReader) getRaw(ctx context.Context, accessToken, id string) ([]byte, error) {
	if g.getRawFn != nil {
		return g.getRawFn(ctx, accessToken, id)
	}
	return graphGetRaw(ctx, accessToken, id)
}

// graphDelta GETs one delta page (either the $deltatoken=latest baseline or the
// stored opaque cursor URL) bound to a static access token — no refresh, the
// fresh token is minted upstream in coreapi. It maps 410 Gone / a resync-required
// 400 to errGraphDeltaExpired so Fetch can re-baseline; other non-2xx report the
// status only (never the body) so a bearer token echoed by Graph never lands in
// logs or errors.
func graphDelta(ctx context.Context, accessToken, u string) ([]deltaMsg, string, string, error) {
	client := oauth2.NewClient(ctx, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: accessToken}))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		return nil, "", "", fmt.Errorf("graph: delta request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", "", fmt.Errorf("graph: delta: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusGone:
		return nil, "", "", errGraphDeltaExpired
	case resp.StatusCode == http.StatusBadRequest && graphResyncRequired(resp.Body):
		return nil, "", "", errGraphDeltaExpired
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return nil, "", "", fmt.Errorf("graph: delta: unexpected status %d", resp.StatusCode)
	}
	var body struct {
		Value     []deltaMsg `json:"value"`
		NextLink  string     `json:"@odata.nextLink"`
		DeltaLink string     `json:"@odata.deltaLink"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, "", "", fmt.Errorf("graph: delta decode: %w", err)
	}
	return body.Value, body.NextLink, body.DeltaLink, nil
}

// graphResyncRequired reports whether a Graph 400 error body is a delta-resync
// signal (code contains "resync" or "syncState"), which — like a 410 — means the
// stored token is unusable and Fetch should re-baseline rather than surface a
// hard error. Only the error code is inspected, never any token-bearing field.
func graphResyncRequired(r io.Reader) bool {
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(r).Decode(&e); err != nil {
		return false
	}
	c := strings.ToLower(e.Error.Code)
	return strings.Contains(c, "resync") || strings.Contains(c, "syncstate")
}

// graphBaseline reads the current top-of-Inbox deltaLink via $deltatoken=latest,
// used to baseline the cursor on a first poll or a delta-expired re-baseline. The
// baseline response carries an empty value and the deltaLink directly (no pages).
func graphBaseline(ctx context.Context, accessToken string) (string, error) {
	_, _, deltaLink, err := graphDelta(ctx, accessToken, graphInboxDeltaURL)
	if err != nil {
		return "", err
	}
	if deltaLink == "" {
		return "", fmt.Errorf("graph: baseline: response missing deltaLink")
	}
	return deltaLink, nil
}

// graphGetRaw fetches one message as RAW RFC822 via /me/messages/{id}/$value.
// Unlike Gmail's format=RAW (base64url JSON), the $value endpoint returns the raw
// MIME bytes directly, so the response body is handed to parseInbound with no
// decode step. A non-2xx reports the status only, never the body.
func graphGetRaw(ctx context.Context, accessToken, id string) ([]byte, error) {
	client := oauth2.NewClient(ctx, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: accessToken}))
	u := graphMessagesURL + "/" + url.PathEscape(id) + "/$value"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("graph: raw request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("graph: raw: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("graph: raw: unexpected status %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("graph: raw read: %w", err)
	}
	return raw, nil
}
