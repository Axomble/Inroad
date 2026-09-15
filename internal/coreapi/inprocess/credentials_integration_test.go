//go:build integration

package inprocess

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/credbroker"
	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/dbtest"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/warmup"
)

// These tests are the end-to-end proof that credential brokering actually
// moved the boundary: an execution-plane coreapi client built with a NIL
// keyring — a process holding no master key at all — still resolves a real
// mailbox credential, because the control plane opens it on the other side of
// an HTTP hop. Docker must be up.

const itBrokerToken = "0123456789abcdef0123456789abcdef" // credbroker.MinTokenLen

func itQuietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// transportResolver is the narrow slice these tests need. ResolveSenderTransport
// is not on coreapi.Client (see its doc for why) and is reached by type
// assertion — the same way internal/worker/testsend reaches it.
type transportResolver interface {
	ResolveSenderTransport(ctx context.Context, workspaceID, mailboxID string) (coreapi.SenderTransport, error)
}

type brokerFixture struct {
	ctx     context.Context
	q       *gen.Queries
	ws      uuid.UUID
	mailbox uuid.UUID
	// remote is the worker's side of the broker: an HTTP client, no key, no
	// claimed worker id — every pre-existing test in this file exercises the
	// unscoped path, unchanged.
	remote *credbroker.HTTPOpener
	// srvURL lets a test build ADDITIONAL openers against the same running
	// broker, claiming a specific worker id — see the assignment-scoping
	// tests below.
	srvURL string
}

// setupBroker builds both planes against one database: a control-plane opener
// with the real keyring, served over httptest, and the worker-side HTTP opener
// that reaches it.
func setupBroker(t *testing.T) brokerFixture {
	t.Helper()
	ctx := context.Background()
	if err := db.Migrate(dbtest.DSN(t)); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := db.Connect(ctx, dbtest.DSN(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	q := gen.New(pool)

	ws, err := q.CreateWorkspace(ctx, "Broker IT "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	// Sealed under the workspace DEK, exactly as the control plane seals at
	// write time — so opening it later genuinely exercises the DEK unwrap
	// rather than the legacy master-key path.
	sealer, err := itKeyring(t, q).SealerFor(ctx, ws.ID)
	if err != nil {
		t.Fatalf("sealer: %v", err)
	}
	ct, err := sealer.Seal([]byte("the-smtp-password"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	email := "broker-" + uuid.NewString() + "@acme.test"
	mb, err := q.CreateMailbox(ctx, gen.CreateMailboxParams{
		WorkspaceID: ws.ID, Provider: "smtp", Email: email, DisplayName: email,
		SmtpHost: "smtp.acme.test", SmtpPort: 587, SmtpUsername: email,
		ImapHost: "imap.acme.test", ImapPort: 993, ImapUsername: email,
		SecretCiphertext: ct, DailyCap: 50, MinIntervalSeconds: 0,
		RampEnabled: false, RampStartCap: 5, RampDays: 30,
	})
	if err != nil {
		t.Fatalf("mailbox: %v", err)
	}

	// The CONTROL plane: the keyring-backed opener, behind the broker handler.
	h, err := credbroker.NewHandler(
		NewCredentialOpener(q, itKeyring(t, q), mail.GoogleOAuth{}, mail.MicrosoftOAuth{}),
		itBrokerToken, itQuietLogger())
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	// http, because httptest speaks http; opted into explicitly.
	remote, err := credbroker.NewHTTPOpener(srv.URL, itBrokerToken, "", true)
	if err != nil {
		t.Fatalf("NewHTTPOpener: %v", err)
	}
	return brokerFixture{ctx: ctx, q: q, ws: ws.ID, mailbox: mb.ID, remote: remote, srvURL: srv.URL}
}

// keylessCore builds the coreapi client a fleet worker gets: NIL keyring, plus
// whatever opener is passed. If any credential path still reached for a local
// key, this client would fail rather than quietly succeed.
func keylessCore(t *testing.T, opts ...Option) transportResolver {
	t.Helper()
	pool, err := db.Connect(context.Background(), dbtest.DSN(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	secret := []byte("0123456789abcdef0123456789abcdef")
	c := New(pool, nil /* no keyring: that is the point */, secret, "https://app.test",
		mail.GoogleOAuth{}, mail.MicrosoftOAuth{}, secret, warmup.NewStaticLibrary(), opts...)
	tr, ok := c.(transportResolver)
	if !ok {
		t.Fatal("the in-process client no longer resolves a sender transport")
	}
	return tr
}

// A worker holding NO master key still gets the real plaintext password,
// because the control plane opened it. This is the whole feature in one
// assertion.
func TestAKeylessWorkerResolvesARealCredentialThroughTheBroker(t *testing.T) {
	f := setupBroker(t)
	core := keylessCore(t, WithCredentialBroker(f.remote))

	tr, err := core.ResolveSenderTransport(f.ctx, f.ws.String(), f.mailbox.String())
	if err != nil {
		t.Fatalf("ResolveSenderTransport: %v", err)
	}
	if string(tr.SMTPPassword) != "the-smtp-password" {
		t.Fatalf("SMTPPassword = %q, want the-smtp-password", tr.SMTPPassword)
	}
	if tr.Provider != "smtp" {
		t.Errorf("Provider = %q, want smtp", tr.Provider)
	}
}

// And a worker with NEITHER a key nor a broker refuses rather than sending
// with something weaker. Fail closed is the rule; this is it at the one place
// a send would actually happen.
func TestAWorkerWithNoCredentialSourceRefusesToResolveATransport(t *testing.T) {
	f := setupBroker(t)
	core := keylessCore(t) // no broker option

	got, err := core.ResolveSenderTransport(f.ctx, f.ws.String(), f.mailbox.String())
	if !errors.Is(err, credbroker.ErrNotConfigured) {
		t.Fatalf("err = %v, want credbroker.ErrNotConfigured", err)
	}
	if len(got.SMTPPassword) != 0 || len(got.AccessToken) != 0 {
		t.Errorf("a refused resolve still returned secret material: %+v", got)
	}
}

// The broker must not open a mailbox for the WRONG workspace: its own re-read
// is workspace-pinned, so a cross-tenant pair opens nothing. The tenant
// boundary does not weaken by moving across the wire.
func TestTheBrokerWillNotOpenACrossTenantMailbox(t *testing.T) {
	f := setupBroker(t)
	foreign, err := f.q.CreateWorkspace(f.ctx, "Broker IT foreign "+uuid.NewString())
	if err != nil {
		t.Fatalf("foreign workspace: %v", err)
	}

	got, err := f.remote.OpenMailbox(f.ctx, credbroker.MailboxRef{WorkspaceID: foreign.ID, MailboxID: f.mailbox})
	if err == nil {
		t.Fatalf("a foreign workspace opened the mailbox: %+v", got)
	}
	if len(got.SMTPPassword) != 0 || len(got.AccessToken) != 0 {
		t.Errorf("a refused open still returned secret material: %+v", got)
	}
}

// The load-bearing test for the credbroker assignment-scoping added in
// docs/competitive-analysis/05-coreapi-http-transport-plan.md's Stage 0: a
// bearer token alone no longer means "any mailbox in the fleet." A worker
// that claims an id NOT live-assigned to this mailbox is refused, even
// though its token is perfectly valid — and the actually-assigned worker is
// unaffected, because this narrows an answer rather than replacing auth.
func TestABrokeredWorkerCannotOpenAMailboxAssignedToAnotherWorker(t *testing.T) {
	f := setupBroker(t)
	if err := f.q.UpsertWorker(f.ctx, gen.UpsertWorkerParams{WorkerID: "worker-a", EgressIp: "203.0.113.1", IDFamily: "ipv4"}); err != nil {
		t.Fatalf("upsert worker-a: %v", err)
	}
	if err := f.q.UpsertWorker(f.ctx, gen.UpsertWorkerParams{WorkerID: "worker-b", EgressIp: "203.0.113.2", IDFamily: "ipv4"}); err != nil {
		t.Fatalf("upsert worker-b: %v", err)
	}
	if _, err := f.q.InsertMailboxWorkerAssignment(f.ctx, gen.InsertMailboxWorkerAssignmentParams{
		MailboxID: f.mailbox, WorkspaceID: f.ws, WorkerID: "worker-a", Band: warmup.RiskBandHealthy, LiveSince: liveSinceNow(),
	}); err != nil {
		t.Fatalf("assign to worker-a: %v", err)
	}

	impersonator, err := credbroker.NewHTTPOpener(f.srvURL, itBrokerToken, "worker-b", true)
	if err != nil {
		t.Fatalf("NewHTTPOpener: %v", err)
	}
	got, err := impersonator.OpenMailbox(f.ctx, credbroker.MailboxRef{WorkspaceID: f.ws, MailboxID: f.mailbox})
	if !errors.Is(err, credbroker.ErrUnauthorized) {
		t.Fatalf("err = %v, want credbroker.ErrUnauthorized (the handler maps the assignment mismatch to 403, same as a bad token, so a caller cannot tell the two apart)", err)
	}
	if len(got.SMTPPassword) != 0 || len(got.AccessToken) != 0 {
		t.Errorf("a refused open still returned secret material: %+v", got)
	}

	// The ACTUALLY assigned worker still opens it fine — confirms this is a
	// narrowing of who can open THIS mailbox, not a general lockout.
	incumbent, err := credbroker.NewHTTPOpener(f.srvURL, itBrokerToken, "worker-a", true)
	if err != nil {
		t.Fatalf("NewHTTPOpener: %v", err)
	}
	if _, err := incumbent.OpenMailbox(f.ctx, credbroker.MailboxRef{WorkspaceID: f.ws, MailboxID: f.mailbox}); err != nil {
		t.Fatalf("the assigned worker was refused its own mailbox: %v", err)
	}
}

// The other half, and the one that keeps this change from breaking the
// smallest legitimate brokered topology: a mailbox with NO live assignment
// at all — the state every single-worker-plus-broker fleet is in, since
// AssignMailboxWorker persists nothing when there is no placement CHOICE to
// make — must still open for whoever asks.
func TestABrokeredWorkerWithNoLiveAssignmentStillOpens(t *testing.T) {
	f := setupBroker(t)
	// No UpsertWorker, no InsertMailboxWorkerAssignment: this mailbox has
	// never been placed at all, exactly the single-worker self-host state.
	solo, err := credbroker.NewHTTPOpener(f.srvURL, itBrokerToken, "the-only-worker", true)
	if err != nil {
		t.Fatalf("NewHTTPOpener: %v", err)
	}
	got, err := solo.OpenMailbox(f.ctx, credbroker.MailboxRef{WorkspaceID: f.ws, MailboxID: f.mailbox})
	if err != nil {
		t.Fatalf("OpenMailbox: %v", err)
	}
	if string(got.SMTPPassword) != "the-smtp-password" {
		t.Fatalf("SMTPPassword = %q, want the-smtp-password", got.SMTPPassword)
	}
}

// The webhook endpoint's HMAC signing secret is the OTHER thing the worker used
// to need a keyring for (GetWebhookDeliveryJob). It brokers too, and — the part
// worth asserting — the broker IGNORES the cached ciphertext the caller passes
// and re-reads the row itself, so a worker cannot name the blob to open.
func TestAWebhookEndpointSecretBrokersAndIgnoresTheCallersCiphertext(t *testing.T) {
	f := setupBroker(t)
	sealer, err := itKeyring(t, f.q).SealerFor(f.ctx, f.ws)
	if err != nil {
		t.Fatalf("sealer: %v", err)
	}
	sealed, err := sealer.Seal([]byte("the-signing-secret"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	ep, err := f.q.CreateWebhookEndpoint(f.ctx, gen.CreateWebhookEndpointParams{
		WorkspaceID: f.ws, Url: "https://receiver.test/hook", Description: "it",
		SecretCiphertext: []byte(sealed), EventTypes: []string{"reply.received"}, Active: true,
	})
	if err != nil {
		t.Fatalf("endpoint: %v", err)
	}

	// A DIFFERENT workspace's ciphertext handed in as the "cache". If the broker
	// honoured it, this would either open the wrong secret or fail — either way
	// the worker would be choosing what gets decrypted.
	foreign, err := f.q.CreateWorkspace(f.ctx, "Broker IT hook "+uuid.NewString())
	if err != nil {
		t.Fatalf("foreign workspace: %v", err)
	}
	foreignSealer, err := itKeyring(t, f.q).SealerFor(f.ctx, foreign.ID)
	if err != nil {
		t.Fatalf("foreign sealer: %v", err)
	}
	decoy, err := foreignSealer.Seal([]byte("not-this-one"))
	if err != nil {
		t.Fatalf("seal decoy: %v", err)
	}

	got, err := f.remote.OpenWebhookEndpointSecret(f.ctx, f.ws, ep.ID, []byte(decoy))
	if err != nil {
		t.Fatalf("OpenWebhookEndpointSecret: %v", err)
	}
	if string(got) != "the-signing-secret" {
		t.Fatalf("secret = %q, want the-signing-secret", got)
	}
}

// Belt and braces on the sealer itself: a field ciphertext is AAD-bound to its
// workspace (docs/security.md invariant 15), so even a broker that somehow read
// the right row under the wrong workspace could not decrypt it.
func TestAWorkspaceSealerCannotOpenAnotherWorkspacesBlob(t *testing.T) {
	f := setupBroker(t)
	foreign, err := f.q.CreateWorkspace(f.ctx, "Broker IT aad "+uuid.NewString())
	if err != nil {
		t.Fatalf("foreign workspace: %v", err)
	}
	row, err := f.q.GetMailbox(f.ctx, gen.GetMailboxParams{ID: f.mailbox, WorkspaceID: f.ws})
	if err != nil {
		t.Fatalf("get mailbox: %v", err)
	}
	foreignSealer, err := itKeyring(t, f.q).SealerFor(f.ctx, foreign.ID)
	if err != nil {
		t.Fatalf("foreign sealer: %v", err)
	}
	if _, err := foreignSealer.Open(row.SecretCiphertext); err == nil {
		t.Fatal("a foreign workspace's sealer opened the blob")
	}
}
