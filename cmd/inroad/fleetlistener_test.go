package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/coreapi"
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

// fakeJobReader is the control plane's job-build half. It answers zero values:
// this file is about which routes are mounted, behind which token, on which
// listener — what the builds return is internal/coreapi/remote's subject.
type fakeJobReader struct{ calls int }

func (f *fakeJobReader) GetStepSendJob(context.Context, string, string) (coreapi.StepSendJob, error) {
	f.calls++
	return coreapi.StepSendJob{}, nil
}

func (f *fakeJobReader) GetInboxPollJob(context.Context, string, string) (coreapi.InboxPollJob, error) {
	f.calls++
	return coreapi.InboxPollJob{}, nil
}

func (f *fakeJobReader) GetWarmupSendJob(context.Context, string, string) (coreapi.WarmupSendJob, error) {
	f.calls++
	return coreapi.WarmupSendJob{}, nil
}

func (f *fakeJobReader) GetWarmupEngageJob(context.Context, string, string) (coreapi.WarmupEngageJob, error) {
	f.calls++
	return coreapi.WarmupEngageJob{}, nil
}

func (f *fakeJobReader) GetWebhookDeliveryJob(context.Context, string, string) (coreapi.WebhookDeliveryJob, error) {
	f.calls++
	return coreapi.WebhookDeliveryJob{}, nil
}

func (f *fakeJobReader) GetTestSendContent(context.Context, string, string, string) (coreapi.TestSendContent, error) {
	f.calls++
	return coreapi.TestSendContent{}, nil
}

func (f *fakeJobReader) ResolveSenderTransport(context.Context, string, string) (coreapi.SenderTransport, error) {
	f.calls++
	return coreapi.SenderTransport{}, nil
}

func (f *fakeJobReader) FindSendByMessageID(context.Context, string, string) (coreapi.SendRef, error) {
	f.calls++
	return coreapi.SendRef{}, nil
}

// fleetRoutes is every route the fleet listener is supposed to mount, with a
// body each accepts. Listing them in ONE place is what makes the "behind the
// token" and "not on the public router" tests grow with the transport instead
// of quietly covering whichever routes existed when they were written.
func fleetRoutes() []struct{ name, path, body string } {
	ws, id := uuid.New().String(), uuid.New().String()
	return []struct{ name, path, body string }{
		{"credentials: mailbox", credbroker.PathMailbox, `{"workspace_id":"` + ws + `","mailbox_id":"` + id + `"}`},
		{"credentials: webhook endpoint", credbroker.PathWebhookEndpoint, `{"workspace_id":"` + ws + `","endpoint_id":"` + id + `"}`},
		{"coreapi: suppression", remote.PathSuppressionCheck, `{"workspace_id":"` + ws + `","email":"ada@example.test"}`},
		{"coreapi: step send job", remote.PathStepSendJob, `{"workspace_id":"` + ws + `","enrollment_id":"` + id + `"}`},
		{"coreapi: inbox poll job", remote.PathInboxPollJob, `{"workspace_id":"` + ws + `","mailbox_id":"` + id + `"}`},
		{"coreapi: warmup send job", remote.PathWarmupSendJob, `{"workspace_id":"` + ws + `","mailbox_id":"` + id + `"}`},
		{"coreapi: warmup engage job", remote.PathWarmupEngageJob, `{"workspace_id":"` + ws + `","receipt_id":"` + id + `"}`},
		{"coreapi: webhook delivery job", remote.PathWebhookDeliveryJob, `{"workspace_id":"` + ws + `","delivery_id":"` + id + `"}`},
		{"coreapi: test send content", remote.PathTestSendContent, `{"workspace_id":"` + ws + `","campaign_id":"` + id + `","step_id":"` + id + `"}`},
		{"coreapi: sender transport", remote.PathSenderTransport, `{"workspace_id":"` + ws + `","mailbox_id":"` + id + `"}`},
		{"coreapi: send by message id", remote.PathSendByMessageID, `{"workspace_id":"` + ws + `","message_id":"<a@b.test>"}`},
	}
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

// The sharing decision, made executable: ONE listener carries both fleet
// transports and ONE token authenticates both. A worker holds a single
// credential and an operator firewalls a single address.
func TestTheFleetListenerServesBothTransportsUnderOneToken(t *testing.T) {
	opener, reader, jobs := &fakeOpener{}, &fakeSuppressionReader{}, &fakeJobReader{}
	h, err := newFleetHandler(fleetDeps{credentials: opener, suppression: reader, jobs: jobs},
		fleetTestToken, discardLogger())
	if err != nil {
		t.Fatalf("newFleetHandler: %v", err)
	}

	for _, route := range fleetRoutes() {
		if code := postFleet(t, h, route.path, fleetTestToken, route.body); code != http.StatusOK {
			t.Errorf("%s (%s): status %d, want 200", route.name, route.path, code)
		}
	}
	if opener.calls != 2 {
		t.Errorf("opener reached %d times, want 2", opener.calls)
	}
	if reader.calls != 1 {
		t.Errorf("suppression reader reached %d times, want 1", reader.calls)
	}
	if jobs.calls != 8 {
		t.Errorf("job reader reached %d times, want 8", jobs.calls)
	}
}

// One token means one rotation — and one refusal. A wrong token is rejected on
// BOTH transports and reaches neither dependency.
func TestTheFleetListenerRejectsAWrongTokenOnBothTransports(t *testing.T) {
	opener, reader, jobs := &fakeOpener{}, &fakeSuppressionReader{}, &fakeJobReader{}
	h, err := newFleetHandler(fleetDeps{credentials: opener, suppression: reader, jobs: jobs},
		fleetTestToken, discardLogger())
	if err != nil {
		t.Fatalf("newFleetHandler: %v", err)
	}

	for _, tc := range fleetRoutes() {
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
	if opener.calls != 0 || reader.calls != 0 || jobs.calls != 0 {
		t.Errorf("dependencies were reached (%d opener, %d reader, %d jobs) despite rejected tokens",
			opener.calls, reader.calls, jobs.calls)
	}
}

// The fleet listener serves the two mounted prefixes and nothing else: it is
// not a second copy of the API, and a path nobody mounted is a 404 rather than
// whatever a catch-all would do.
func TestTheFleetListenerServesNothingElse(t *testing.T) {
	h, err := newFleetHandler(
		fleetDeps{credentials: &fakeOpener{}, suppression: &fakeSuppressionReader{}, jobs: &fakeJobReader{}},
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

// A listener missing ANY transport refuses to be built. Half a fleet channel
// would start, serve the half it has, and fail every call to the other at the
// first send — which is #216's lesson in a different shape.
func TestTheFleetListenerRefusesAMissingTransport(t *testing.T) {
	for _, tc := range []struct {
		name string
		deps fleetDeps
	}{
		{"no credentials", fleetDeps{suppression: &fakeSuppressionReader{}, jobs: &fakeJobReader{}}},
		{"no suppression", fleetDeps{credentials: &fakeOpener{}, jobs: &fakeJobReader{}}},
		{"no jobs", fleetDeps{credentials: &fakeOpener{}, suppression: &fakeSuppressionReader{}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := newFleetHandler(tc.deps, fleetTestToken, discardLogger()); err == nil {
				t.Error("newFleetHandler accepted a half-wired listener, want an error")
			}
		})
	}
}

// And the other half of that rule: the PUBLIC API router serves no fleet route
// at all. These endpoints hand a machine credential holder decrypted secrets
// and a tenant's compliance state; they must never sit behind the public
// listener, where an operator's firewall is not what protects them.
func TestThePublicAPIRouterServesNoFleetRoute(t *testing.T) {
	r := buildRouter(discardLogger(), nil, nil, nil)
	for _, route := range fleetRoutes() {
		if code := postFleet(t, r, route.path, fleetTestToken, route.body); code != http.StatusNotFound {
			t.Errorf("the public router answered %s with %d, want 404", route.path, code)
		}
	}
}

// A weak token is refused when the listener is BUILT, naming the variable,
// rather than at the first request as a 401 nobody is watching for.
func TestTheFleetListenerRefusesAWeakToken(t *testing.T) {
	_, err := newFleetHandler(
		fleetDeps{credentials: &fakeOpener{}, suppression: &fakeSuppressionReader{}, jobs: &fakeJobReader{}},
		strings.Repeat("a", credbroker.MinTokenLen-1), discardLogger())
	if err == nil {
		t.Fatal("newFleetHandler accepted a short token, want an error")
	}
}
