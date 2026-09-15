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
	// remote is the worker's side of the broker: an HTTP client, no key.
	remote *credbroker.HTTPOpener
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
	remote, err := credbroker.NewHTTPOpener(srv.URL, itBrokerToken, true)
	if err != nil {
		t.Fatalf("NewHTTPOpener: %v", err)
	}
	return brokerFixture{ctx: ctx, q: q, ws: ws.ID, mailbox: mb.ID, remote: remote}
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
