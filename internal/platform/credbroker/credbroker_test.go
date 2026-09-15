package credbroker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// fakeOpener is the control plane's side in these tests: it records what it was
// asked for and returns a fixed answer.
type fakeOpener struct {
	gotRef    MailboxRef
	gotWS     uuid.UUID
	gotID     uuid.UUID
	gotSealed []byte
	secret    MailboxSecret
	webhook   []byte
	err       error
}

func (f *fakeOpener) OpenMailbox(_ context.Context, ref MailboxRef) (MailboxSecret, error) {
	f.gotRef = ref
	return f.secret, f.err
}

func (f *fakeOpener) OpenWebhookEndpointSecret(_ context.Context, ws, id uuid.UUID, sealed []byte) ([]byte, error) {
	f.gotWS, f.gotID, f.gotSealed = ws, id, sealed
	return f.webhook, f.err
}

const testToken = "0123456789abcdef0123456789abcdef" // 32 bytes, the floor

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// serve stands up the handler on an httptest server and returns a client
// pointed at it. allowPlaintext is true because httptest speaks http.
func serve(t *testing.T, o Opener, token string) (*HTTPOpener, *httptest.Server) {
	t.Helper()
	h, err := NewHandler(o, testToken, quietLogger())
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := NewHTTPOpener(srv.URL, token, "", true)
	if err != nil {
		t.Fatalf("NewHTTPOpener: %v", err)
	}
	return c, srv
}

func TestMailboxCredentialRoundTrips(t *testing.T) {
	ws, mb := uuid.New(), uuid.New()
	f := &fakeOpener{secret: MailboxSecret{Provider: "smtp", SMTPPassword: []byte("hunter2")}}
	c, _ := serve(t, f, testToken)

	got, err := c.OpenMailbox(context.Background(), MailboxRef{
		WorkspaceID: ws, MailboxID: mb,
		// Supplied by the caller and expected NOT to survive the hop.
		Provider: "smtp", Sealed: "some-ciphertext",
	})
	if err != nil {
		t.Fatalf("OpenMailbox: %v", err)
	}
	if got.Provider != "smtp" || string(got.SMTPPassword) != "hunter2" {
		t.Fatalf("got %+v, want smtp/hunter2", got)
	}
	if got.AccessToken != nil {
		t.Errorf("access_token = %q, want nil for an smtp mailbox", got.AccessToken)
	}
	if f.gotRef.WorkspaceID != ws || f.gotRef.MailboxID != mb {
		t.Errorf("server saw %v/%v, want %v/%v", f.gotRef.WorkspaceID, f.gotRef.MailboxID, ws, mb)
	}
}

// The load-bearing property of the wire contract: a worker cannot name the
// ciphertext to decrypt. If Sealed or Provider crossed the wire, the broker
// would be a general decryption oracle and moving the key would buy nothing.
func TestTheClientNeverSendsCiphertextOrProvider(t *testing.T) {
	f := &fakeOpener{secret: MailboxSecret{Provider: "gmail", AccessToken: []byte("ya29.token")}}
	c, _ := serve(t, f, testToken)

	if _, err := c.OpenMailbox(context.Background(), MailboxRef{
		WorkspaceID: uuid.New(), MailboxID: uuid.New(),
		Provider: "smtp", Sealed: "AgABBB-attacker-chosen-ciphertext",
	}); err != nil {
		t.Fatalf("OpenMailbox: %v", err)
	}
	if f.gotRef.Sealed != "" {
		t.Errorf("server received Sealed = %q, want empty", f.gotRef.Sealed)
	}
	if f.gotRef.Provider != "" {
		t.Errorf("server received Provider = %q, want empty", f.gotRef.Provider)
	}
}

// Same property, asserted on the bytes rather than on the fake: the request
// body must contain the two ids and nothing else.
func TestTheRequestBodyCarriesIdsOnly(t *testing.T) {
	ws, mb := uuid.New(), uuid.New()
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(mailboxResponse{Provider: "smtp"})
	}))
	defer srv.Close()
	c, err := NewHTTPOpener(srv.URL, testToken, "", true)
	if err != nil {
		t.Fatalf("NewHTTPOpener: %v", err)
	}
	if _, err := c.OpenMailbox(context.Background(), MailboxRef{
		WorkspaceID: ws, MailboxID: mb, Provider: "smtp", Sealed: "ciphertext",
	}); err != nil {
		t.Fatalf("OpenMailbox: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatalf("unmarshal %q: %v", body, err)
	}
	// worker_id is omitempty and this opener was built with none, so the body
	// stays exactly workspace_id + mailbox_id — an empty claim adds nothing to
	// the wire, matching MailboxRef.WorkerID's documented no-op behavior.
	if len(fields) != 2 || fields["workspace_id"] != ws.String() || fields["mailbox_id"] != mb.String() {
		t.Errorf("request body = %s, want exactly workspace_id + mailbox_id", body)
	}
}

// A configured worker id DOES cross the wire — the opt-in half of the
// property above. The opener never authenticates on its own (the bearer
// token still does that); this only carries the claim to wherever it gets
// checked.
func TestAConfiguredWorkerIDCrossesTheWire(t *testing.T) {
	ws, mb := uuid.New(), uuid.New()
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(mailboxResponse{Provider: "smtp"})
	}))
	defer srv.Close()
	c, err := NewHTTPOpener(srv.URL, testToken, "worker-abc", true)
	if err != nil {
		t.Fatalf("NewHTTPOpener: %v", err)
	}
	if _, err := c.OpenMailbox(context.Background(), MailboxRef{WorkspaceID: ws, MailboxID: mb}); err != nil {
		t.Fatalf("OpenMailbox: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatalf("unmarshal %q: %v", body, err)
	}
	if fields["worker_id"] != "worker-abc" {
		t.Errorf("request body = %s, want worker_id = worker-abc", body)
	}
}

func TestWebhookEndpointSecretRoundTrips(t *testing.T) {
	ws, ep := uuid.New(), uuid.New()
	f := &fakeOpener{webhook: []byte("signing-secret")}
	c, _ := serve(t, f, testToken)

	got, err := c.OpenWebhookEndpointSecret(context.Background(), ws, ep, []byte("cached-ciphertext"))
	if err != nil {
		t.Fatalf("OpenWebhookEndpointSecret: %v", err)
	}
	if string(got) != "signing-secret" {
		t.Errorf("secret = %q, want signing-secret", got)
	}
	if f.gotWS != ws || f.gotID != ep {
		t.Errorf("server saw %v/%v, want %v/%v", f.gotWS, f.gotID, ws, ep)
	}
	if f.gotSealed != nil {
		t.Errorf("server received sealed = %q, want nil", f.gotSealed)
	}
}

func TestAWrongTokenIsRejectedAndOpensNothing(t *testing.T) {
	f := &fakeOpener{secret: MailboxSecret{Provider: "smtp", SMTPPassword: []byte("hunter2")}}
	c, _ := serve(t, f, strings.Repeat("z", MinTokenLen))

	_, err := c.OpenMailbox(context.Background(), MailboxRef{WorkspaceID: uuid.New(), MailboxID: uuid.New()})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
	if f.gotRef.MailboxID != uuid.Nil {
		t.Errorf("the opener was reached despite a bad token (ref %+v)", f.gotRef)
	}
}

func TestAMissingAuthorizationHeaderIsRejected(t *testing.T) {
	h, err := NewHandler(&fakeOpener{}, testToken, quietLogger())
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		srv.URL+PathMailbox, bytes.NewReader([]byte(`{"workspace_id":"x","mailbox_id":"y"}`)))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestAMissingSubjectIsNotFoundAndLeaksNoDetail(t *testing.T) {
	f := &fakeOpener{err: pgx.ErrNoRows}
	c, srv := serve(t, f, testToken)

	_, err := c.OpenMailbox(context.Background(), MailboxRef{WorkspaceID: uuid.New(), MailboxID: uuid.New()})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}

	// And the body says nothing about why.
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+PathMailbox,
		strings.NewReader(`{"workspace_id":"`+uuid.New().String()+`","mailbox_id":"`+uuid.New().String()+`"}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), "no rows") {
		t.Errorf("body leaked the database error: %s", body)
	}
}

// An opener failure that is not "missing" must not become a 404 — a worker that
// treated a transient control-plane fault as "this mailbox is gone" would drop
// the send instead of retrying it.
func TestAnOpenFailureIsNotMistakenForNotFound(t *testing.T) {
	c, _ := serve(t, &fakeOpener{err: errors.New("dek unwrap failed")}, testToken)

	_, err := c.OpenMailbox(context.Background(), MailboxRef{WorkspaceID: uuid.New(), MailboxID: uuid.New()})
	if err == nil {
		t.Fatal("want an error")
	}
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want a generic transport error", err)
	}
	if strings.Contains(err.Error(), "dek unwrap failed") {
		t.Errorf("the control plane's error text reached the worker: %v", err)
	}
}

func TestAPlaintextBrokerURLIsRefusedUnlessChosen(t *testing.T) {
	for _, tc := range []struct {
		name           string
		url            string
		allowPlaintext bool
		wantErr        error
	}{
		{"http refused by default", "http://control.internal:8090", false, ErrInsecureURL},
		{"http allowed when chosen", "http://control.internal:8090", true, nil},
		{"https always fine", "https://control.example", false, nil},
		{"no scheme is refused, never assumed", "control.example:8090", false, ErrInsecureURL},
		{"a non-http scheme is refused", "ftp://control.example", true, ErrInsecureURL},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewHTTPOpener(tc.url, testToken, "", tc.allowPlaintext)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("err = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestAWeakTokenIsRefusedOnBothSides(t *testing.T) {
	short := strings.Repeat("a", MinTokenLen-1)
	if _, err := NewHTTPOpener("https://control.example", short, "", false); !errors.Is(err, ErrWeakToken) {
		t.Errorf("client err = %v, want ErrWeakToken", err)
	}
	if _, err := NewHandler(&fakeOpener{}, short, quietLogger()); !errors.Is(err, ErrWeakToken) {
		t.Errorf("handler err = %v, want ErrWeakToken", err)
	}
}

// The zero-configuration opener must refuse, not panic and not succeed.
func TestUnconfiguredFailsClosed(t *testing.T) {
	var o Opener = Unconfigured{}
	if _, err := o.OpenMailbox(context.Background(), MailboxRef{}); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("OpenMailbox err = %v, want ErrNotConfigured", err)
	}
	if _, err := o.OpenWebhookEndpointSecret(context.Background(), uuid.Nil, uuid.Nil, nil); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("OpenWebhookEndpointSecret err = %v, want ErrNotConfigured", err)
	}
}

// A redirect is never followed: doing so would replay the bearer token, and
// accept a credential, from wherever the redirect pointed.
func TestARedirectIsNotFollowed(t *testing.T) {
	var hits int
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(mailboxResponse{Provider: "smtp", SMTPPassword: []byte("stolen")})
	}))
	defer evil.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, evil.URL+PathMailbox, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	c, err := NewHTTPOpener(redirector.URL, testToken, "", true)
	if err != nil {
		t.Fatalf("NewHTTPOpener: %v", err)
	}
	if _, err := c.OpenMailbox(context.Background(), MailboxRef{WorkspaceID: uuid.New(), MailboxID: uuid.New()}); err == nil {
		t.Fatal("want an error, got a credential from the redirect target")
	}
	if hits != 0 {
		t.Errorf("the redirect target was dialed %d times, want 0", hits)
	}
}
