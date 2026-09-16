package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/coreapi/remote"
	"github.com/inroad/inroad/internal/platform/credbroker"
)

const fleetTestToken = "0123456789abcdef0123456789abcdef" // credbroker.MinTokenLen

type fakeOpener struct{ calls int }

func (f *fakeOpener) OpenMailbox(context.Context, credbroker.MailboxRef) (credbroker.MailboxSecret, error) {
	f.calls++
	return credbroker.MailboxSecret{Provider: "smtp", SMTPPassword: []byte("hunter2")}, nil
}

func (f *fakeOpener) OpenWebhookEndpointSecret(context.Context, uuid.UUID, uuid.UUID, []byte) ([]byte, error) {
	f.calls++
	return []byte("secret"), nil
}

type fakeSuppressionReader struct{ calls int }

func (f *fakeSuppressionReader) IsSuppressed(context.Context, uuid.UUID, string) (bool, error) {
	f.calls++
	return true, nil
}

func postFleet(t *testing.T, h http.Handler, path, token, body string) int {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w.Code
}

func mailboxBody() string {
	return `{"workspace_id":"` + uuid.New().String() + `","mailbox_id":"` + uuid.New().String() + `"}`
}

func suppressionBody() string {
	return `{"workspace_id":"` + uuid.New().String() + `","email":"ada@example.test"}`
}

// The sharing decision, made executable: ONE listener carries both fleet
// transports and ONE token authenticates both. A worker holds a single
// credential and an operator firewalls a single address.
func TestTheFleetListenerServesBothTransportsUnderOneToken(t *testing.T) {
	opener, reader := &fakeOpener{}, &fakeSuppressionReader{}
	h, err := newFleetHandler(fleetDeps{credentials: opener, suppression: reader}, fleetTestToken, discardLogger())
	if err != nil {
		t.Fatalf("newFleetHandler: %v", err)
	}

	if code := postFleet(t, h, credbroker.PathMailbox, fleetTestToken, mailboxBody()); code != http.StatusOK {
		t.Errorf("credential broker: status %d, want 200", code)
	}
	if code := postFleet(t, h, remote.PathSuppressionCheck, fleetTestToken, suppressionBody()); code != http.StatusOK {
		t.Errorf("coreapi transport: status %d, want 200", code)
	}
	if opener.calls != 1 {
		t.Errorf("opener reached %d times, want 1", opener.calls)
	}
	if reader.calls != 1 {
		t.Errorf("suppression reader reached %d times, want 1", reader.calls)
	}
}

// One token means one rotation — and one refusal. A wrong token is rejected on
// BOTH transports and reaches neither dependency.
func TestTheFleetListenerRejectsAWrongTokenOnBothTransports(t *testing.T) {
	opener, reader := &fakeOpener{}, &fakeSuppressionReader{}
	h, err := newFleetHandler(fleetDeps{credentials: opener, suppression: reader}, fleetTestToken, discardLogger())
	if err != nil {
		t.Fatalf("newFleetHandler: %v", err)
	}

	for _, tc := range []struct{ name, path, body string }{
		{"credential broker", credbroker.PathMailbox, mailboxBody()},
		{"coreapi transport", remote.PathSuppressionCheck, suppressionBody()},
	} {
		t.Run(tc.name+"/wrong token", func(t *testing.T) {
			if code := postFleet(t, h, tc.path, strings.Repeat("z", credbroker.MinTokenLen), tc.body); code != http.StatusUnauthorized {
				t.Errorf("status %d, want 401", code)
			}
		})
		t.Run(tc.name+"/no token", func(t *testing.T) {
			if code := postFleet(t, h, tc.path, "", tc.body); code != http.StatusUnauthorized {
				t.Errorf("status %d, want 401", code)
			}
		})
	}
	if opener.calls != 0 || reader.calls != 0 {
		t.Errorf("dependencies were reached (%d opener, %d reader) despite rejected tokens", opener.calls, reader.calls)
	}
}

// The fleet listener serves the two mounted prefixes and nothing else: it is
// not a second copy of the API, and a path nobody mounted is a 404 rather than
// whatever a catch-all would do.
func TestTheFleetListenerServesNothingElse(t *testing.T) {
	h, err := newFleetHandler(fleetDeps{credentials: &fakeOpener{}, suppression: &fakeSuppressionReader{}},
		fleetTestToken, discardLogger())
	if err != nil {
		t.Fatalf("newFleetHandler: %v", err)
	}
	for _, path := range []string{"/", "/api/v1/campaigns", "/internal/fleet/", "/healthz"} {
		if code := postFleet(t, h, path, fleetTestToken, "{}"); code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", path, code)
		}
	}
}

// And the other half of that rule: the PUBLIC API router serves no fleet route
// at all. These endpoints hand a machine credential holder decrypted secrets
// and a tenant's compliance state; they must never sit behind the public
// listener, where an operator's firewall is not what protects them.
func TestThePublicAPIRouterServesNoFleetRoute(t *testing.T) {
	r := buildRouter(discardLogger(), nil, nil, nil)
	for _, path := range []string{credbroker.PathMailbox, credbroker.PathWebhookEndpoint, remote.PathSuppressionCheck} {
		if code := postFleet(t, r, path, fleetTestToken, "{}"); code != http.StatusNotFound {
			t.Errorf("the public router answered %s with %d, want 404", path, code)
		}
	}
}

// A weak token is refused when the listener is BUILT, naming the variable,
// rather than at the first request as a 401 nobody is watching for.
func TestTheFleetListenerRefusesAWeakToken(t *testing.T) {
	_, err := newFleetHandler(fleetDeps{credentials: &fakeOpener{}, suppression: &fakeSuppressionReader{}},
		strings.Repeat("a", credbroker.MinTokenLen-1), discardLogger())
	if err == nil {
		t.Fatal("newFleetHandler accepted a short token, want an error")
	}
}
