package mail

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// providerReply answers every Gmail and Graph call these tests make. One body
// serves both providers: each decoder reads only the fields it knows and ignores
// the rest, so there is no per-endpoint canned-response table to keep in sync.
const providerReply = `{
  "id": "draft-1",
  "historyId": "42",
  "messages": [],
  "internetMessageId": "<assigned@exchange.example>",
  "value": [],
  "@odata.deltaLink": "https://graph.microsoft.com/v1.0/me/mailFolders('inbox')/messages/delta?$deltatoken=x"
}`

// recordingTransport answers every request from providerReply and records the
// URLs it was asked for. Injected as a component's BASE client, it is the only
// way a provider call in these tests can succeed — a leg that builds its own
// client instead (oauth2.NewClient over http.DefaultTransport: the defect this
// file guards) dials the real API host and leaves this recorder empty.
type recordingTransport struct {
	mu   sync.Mutex
	urls []string
}

func (rt *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.mu.Lock()
	rt.urls = append(rt.urls, req.URL.String())
	rt.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(providerReply)),
		Request:    req,
	}, nil
}

func (rt *recordingTransport) seen() []string {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return append([]string(nil), rt.urls...)
}

// TestProviderAPILegsDialThroughTheConfiguredClient is the test that fails if
// any of the five provider-API legs goes back to building its own HTTP client.
// The egress binding and the chosen timeouts live on ONE client (newAPIHTTPClient);
// a leg that does not route through the client it was constructed with gets
// neither, silently, which is exactly how INROAD_WORKER_EGRESS_IP came to be a
// no-op for API-backed mailboxes while every test stayed green.
func TestProviderAPILegsDialThroughTheConfiguredClient(t *testing.T) {
	msg := Message{FromEmail: "rep@example.com", To: "lead@example.com", Subject: "hi", BodyText: "hi"}

	tests := []struct {
		name     string
		call     func(ctx context.Context, base *http.Client) error
		wantHost string
	}{
		{
			name: "gmail sender",
			call: func(ctx context.Context, base *http.Client) error {
				_, err := (&GmailSender{httpClient: base}).Send(ctx, "tok", msg)
				return err
			},
			wantHost: "gmail.googleapis.com",
		},
		{
			name: "gmail reader",
			call: func(ctx context.Context, base *http.Client) error {
				_, _, err := (&GmailReader{httpClient: base}).Fetch(ctx, "tok", "", 10)
				return err
			},
			wantHost: "gmail.googleapis.com",
		},
		{
			name: "gmail engager",
			call: func(ctx context.Context, base *http.Client) error {
				return (&GmailEngager{httpClient: base}).MarkRead(ctx, EngageTarget{
					Provider: "gmail", AccessToken: []byte("tok"), MessageID: "<m@inroad>",
				})
			},
			wantHost: "gmail.googleapis.com",
		},
		{
			name: "graph sender",
			call: func(ctx context.Context, base *http.Client) error {
				_, err := (&GraphSender{httpClient: base}).Send(ctx, "tok", msg)
				return err
			},
			wantHost: "graph.microsoft.com",
		},
		{
			name: "graph reader",
			call: func(ctx context.Context, base *http.Client) error {
				_, _, err := (&GraphReader{httpClient: base}).Fetch(ctx, "tok", "", 10)
				return err
			},
			wantHost: "graph.microsoft.com",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rt := &recordingTransport{}
			if err := tc.call(t.Context(), &http.Client{Transport: rt}); err != nil {
				t.Fatalf("call failed: %v", err)
			}
			seen := rt.seen()
			if len(seen) == 0 {
				t.Fatal("the injected client saw no request: this leg built its own HTTP client, so the egress binding and timeouts did not apply to it")
			}
			for _, u := range seen {
				if !strings.Contains(u, tc.wantHost) {
					t.Fatalf("request went to %q, want the fixed %s host", u, tc.wantHost)
				}
			}
		})
	}
}
