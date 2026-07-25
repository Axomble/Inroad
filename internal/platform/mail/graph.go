package mail

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"golang.org/x/oauth2"
)

// graphMessagesURL is the fixed Microsoft Graph collection for the signed-in
// user's messages. Draft creation POSTs here; per-message send/delete append
// the draft id. The host is not user input, so no SSRF vetting is needed
// (mirrors GmailSender's use of Google's fixed API host).
const graphMessagesURL = "https://graph.microsoft.com/v1.0/me/messages"

// GraphSender sends mail through the Microsoft Graph API using a per-call access
// token. No SSRF vetting: the host is Graph's fixed API endpoint, not user input.
type GraphSender struct {
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

// NewGraphSender returns a GraphSender that talks to the real Graph API.
func NewGraphSender() *GraphSender { return &GraphSender{} }

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

	createDraft := g.createDraftFn
	if createDraft == nil {
		createDraft = createGraphDraft
	}
	sendDraft := g.sendDraftFn
	if sendDraft == nil {
		sendDraft = sendGraphDraft
	}
	deleteDraft := g.deleteDraftFn
	if deleteDraft == nil {
		deleteDraft = deleteGraphDraft
	}

	id, internetMessageID, err := createDraft(ctx, accessToken, []byte(enc))
	if err != nil {
		return "", err
	}
	if err := sendDraft(ctx, accessToken, id); err != nil {
		// The draft was created but never sent. Best-effort delete so a failed
		// send doesn't leave an orphaned draft in the user's mailbox; ignore the
		// delete outcome (it's cleanup, not the operation's result).
		_ = deleteDraft(ctx, accessToken, id)
		return "", err
	}
	return internetMessageID, nil
}

// createGraphDraft POSTs the base64 MIME to /me/messages. Graph parses the MIME
// into a draft and, on 201 Created, returns JSON including our draft id and the
// internetMessageId Exchange assigned (the authoritative Message-ID). A
// non-2xx reports the status only — never the response body — so a bearer token
// echoed by Graph never lands in logs or errors.
func createGraphDraft(ctx context.Context, accessToken string, rawB64 []byte) (string, string, error) {
	client := oauth2.NewClient(ctx, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: accessToken}))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, graphMessagesURL, bytes.NewReader(rawB64))
	if err != nil {
		return "", "", fmt.Errorf("graph: draft request: %w", err)
	}
	req.Header.Set("Content-Type", "text/plain")
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("graph: draft: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", fmt.Errorf("graph: draft: unexpected status %d", resp.StatusCode)
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
func sendGraphDraft(ctx context.Context, accessToken, id string) error {
	client := oauth2.NewClient(ctx, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: accessToken}))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, graphMessagesURL+"/"+url.PathEscape(id)+"/send", http.NoBody)
	if err != nil {
		return fmt.Errorf("graph: send request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("graph: send: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("graph: send: unexpected status %d", resp.StatusCode)
	}
	return nil
}

// deleteGraphDraft best-effort removes an unsent draft after a failed send.
// The caller ignores the result; errors here are not the operation's outcome.
func deleteGraphDraft(ctx context.Context, accessToken, id string) error {
	client := oauth2.NewClient(ctx, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: accessToken}))
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, graphMessagesURL+"/"+url.PathEscape(id), http.NoBody)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}
