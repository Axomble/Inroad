package mail

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
)

// graphMessagesURL is the fixed Microsoft Graph collection for the signed-in
// user's messages. Draft creation POSTs here; per-message send/delete append
// the draft id. The host is not user input, so no SSRF vetting is needed
// (mirrors GmailSender's use of Google's fixed API host).
const graphMessagesURL = "https://graph.microsoft.com/v1.0/me/messages"

// GraphSender sends mail through the Microsoft Graph API using a per-call access
// token. No SSRF vetting: the host is Graph's fixed API endpoint, not user input.
type GraphSender struct {
	// httpClient is the egress-bound, timeout-bounded client every API call
	// dials through (newAPIHTTPClient). Built once by NewGraphSender and reused,
	// so the draft/send/delete legs of one send share a connection.
	httpClient *http.Client
	// createDraftFn creates a draft from the base64-encoded RFC822 message and
	// returns the draft id + the AUTHORITATIVE internetMessageId Exchange
	// assigned. sendDraftFn sends the created draft. deleteDraftFn best-effort
	// removes a created-but-unsent draft after a send failure. nil selects the
	// real Graph calls (createGraphDraft/sendGraphDraft/deleteGraphDraft); tests
	// stub them to assert the two-step flow (and the cleanup delete) and the
	// returned id without a network round trip. Mirrors the dial seam
	// NetSender/GmailSender use to stay unit-testable.
	createDraftFn func(ctx context.Context, accessToken string, rawB64 []byte) (id, internetMessageID string, err error)
	sendDraftFn   func(ctx context.Context, accessToken, id string) error
	deleteDraftFn func(ctx context.Context, accessToken, id string) error
}

// NewGraphSender returns a GraphSender that talks to the real Graph API,
// egressing from localAddr (mail.ParseEgressIP; nil = OS default route). See
// NewGmailSender for why the address is a constructor argument rather than a
// settable field.
func NewGraphSender(localAddr *net.TCPAddr) *GraphSender {
	return &GraphSender{httpClient: newAPIHTTPClient(localAddr)}
}

// Send builds the RFC822 message (reusing buildMessage — same headers,
// threading, body as the SMTP and Gmail paths), then runs Graph's two-step MIME
// send: create a draft (Graph parses the MIME and assigns the authoritative
// internetMessageId — Exchange may rewrite the Message-Id we supplied, so we
// must NOT trust our own header), then send that draft by id.
//
// The returned id is the internetMessageId from draft creation — this is what
// inbound replies' In-Reply-To/References will reference and what we store as
// sends.message_id, so reply/bounce matching (FindSendByMessageID) keys on the
// value Exchange actually used, not the one we asked for.
//
// The MIME is base64-encoded with STANDARD base64 (per Graph's contract — NOT
// the URL encoding Gmail uses).
func (g *GraphSender) Send(ctx context.Context, accessToken string, msg Message) (string, error) {
	m, err := buildMessage(msg)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if _, err := m.WriteTo(&buf); err != nil {
		return "", fmt.Errorf("graph: serialize: %w", err)
	}
	enc := base64.StdEncoding.EncodeToString(buf.Bytes())

	id, internetMessageID, err := g.createDraft(ctx, accessToken, []byte(enc))
	if err != nil {
		return "", err
	}
	if err := g.sendDraft(ctx, accessToken, id); err != nil {
		// The draft was created but never sent. Best-effort delete so a failed
		// send doesn't leave an orphaned draft in the user's mailbox; ignore the
		// delete outcome (it's cleanup, not the operation's result).
		_ = g.deleteDraft(ctx, accessToken, id)
		return "", err
	}
	return internetMessageID, nil
}

// The three accessors below dispatch to a test's stub when one is set, otherwise
// to the real wire call with this sender's egress-bound client. Mirrors the
// accessor shape GmailReader/GmailEngager already use, which is what keeps the
// client threaded through the seam instead of being rebuilt inside each call.

func (g *GraphSender) createDraft(ctx context.Context, accessToken string, rawB64 []byte) (string, string, error) {
	if g.createDraftFn != nil {
		return g.createDraftFn(ctx, accessToken, rawB64)
	}
	return createGraphDraft(ctx, g.httpClient, accessToken, rawB64)
}

func (g *GraphSender) sendDraft(ctx context.Context, accessToken, id string) error {
	if g.sendDraftFn != nil {
		return g.sendDraftFn(ctx, accessToken, id)
	}
	return sendGraphDraft(ctx, g.httpClient, accessToken, id)
}

func (g *GraphSender) deleteDraft(ctx context.Context, accessToken, id string) error {
	if g.deleteDraftFn != nil {
		return g.deleteDraftFn(ctx, accessToken, id)
	}
	return deleteGraphDraft(ctx, g.httpClient, accessToken, id)
}

// createGraphDraft POSTs the base64 MIME to /me/messages. Graph parses the MIME
// into a draft and, on 201 Created, returns JSON including our draft id and the
// internetMessageId Exchange assigned (the authoritative Message-ID). A
// non-2xx reports the status only — never the response body — so a bearer token
// echoed by Graph never lands in logs or errors.
func createGraphDraft(ctx context.Context, hc *http.Client, accessToken string, rawB64 []byte) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, graphMessagesURL, bytes.NewReader(rawB64))
	if err != nil {
		return "", "", fmt.Errorf("graph: draft request: %w", err)
	}
	req.Header.Set("Content-Type", "text/plain")
	resp, err := bearerClient(ctx, hc, accessToken).Do(req)
	if err != nil {
		return "", "", fmt.Errorf("graph: draft: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// The status travels as DATA (see APIError) so the fleet's signal
		// collector can classify a 429 throttle without parsing this sentence.
		// Reason stays empty: Graph's error body is deliberately never read, and
		// that rule is what keeps an echoed bearer token out of logs.
		return "", "", &APIError{Provider: "m365", Op: "draft", Status: resp.StatusCode}
	}
	var body struct {
		ID                string `json:"id"`
		InternetMessageID string `json:"internetMessageId"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", "", fmt.Errorf("graph: draft decode: %w", err)
	}
	if body.ID == "" {
		return "", "", fmt.Errorf("graph: draft: response missing message id")
	}
	// An empty internetMessageId would be stored as sends.message_id on a "sent"
	// row and silently break reply/bounce matching (Task 4). Capturing the
	// authoritative Message-ID is the whole point of draft-then-send, so fail the
	// send rather than persist an unmatchable value.
	if body.InternetMessageID == "" {
		return "", "", fmt.Errorf("graph: draft created without internetMessageId")
	}
	return body.ID, body.InternetMessageID, nil
}

// sendGraphDraft POSTs to /me/messages/{id}/send with an empty body. A 202
// Accepted (any 2xx) is success. Non-2xx reports status only.
func sendGraphDraft(ctx context.Context, hc *http.Client, accessToken, id string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, graphMessagesURL+"/"+url.PathEscape(id)+"/send", http.NoBody)
	if err != nil {
		return fmt.Errorf("graph: send request: %w", err)
	}
	resp, err := bearerClient(ctx, hc, accessToken).Do(req)
	if err != nil {
		return fmt.Errorf("graph: send: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Status as data, body never read — same reasoning as createGraphDraft.
		return &APIError{Provider: "m365", Op: "send", Status: resp.StatusCode}
	}
	return nil
}

// deleteGraphDraft best-effort removes an unsent draft after a failed send.
// The caller ignores the result; errors here are not the operation's outcome.
func deleteGraphDraft(ctx context.Context, hc *http.Client, accessToken, id string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, graphMessagesURL+"/"+url.PathEscape(id), http.NoBody)
	if err != nil {
		return err
	}
	resp, err := bearerClient(ctx, hc, accessToken).Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}
