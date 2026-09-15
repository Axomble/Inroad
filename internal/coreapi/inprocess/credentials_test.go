package inprocess

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/credbroker"
	"github.com/inroad/inroad/internal/platform/mail"
)

// stubOpener answers whatever it is told to, including a wrong answer.
type stubOpener struct {
	sec credbroker.MailboxSecret
	err error
}

func (s stubOpener) OpenMailbox(context.Context, credbroker.MailboxRef) (credbroker.MailboxSecret, error) {
	return s.sec, s.err
}

func (s stubOpener) OpenWebhookEndpointSecret(context.Context, uuid.UUID, uuid.UUID, []byte) ([]byte, error) {
	return nil, s.err
}

// An opener that answers about a DIFFERENT provider than the row the caller read
// is not looking at the same mailbox. Dialing SMTP with an access token — or
// Gmail with a password — is not a failure worth discovering at the provider.
func TestAProviderMismatchFromTheOpenerIsRefused(t *testing.T) {
	c := client{creds: stubOpener{sec: credbroker.MailboxSecret{
		Provider: "gmail", AccessToken: []byte("ya29.wrong-mailbox"),
	}}}

	at, pw, err := c.openMailboxSecret(context.Background(), uuid.New(), uuid.New(), "smtp", "ciphertext")
	if err == nil {
		t.Fatal("a mismatched provider was accepted")
	}
	if !strings.Contains(err.Error(), "provider") {
		t.Errorf("err = %v, want it to name the provider mismatch", err)
	}
	if at != nil || pw != nil {
		t.Errorf("a refused open still returned secret material: at=%q pw=%q", at, pw)
	}
}

func TestAMatchingProviderPassesThrough(t *testing.T) {
	c := client{creds: stubOpener{sec: credbroker.MailboxSecret{
		Provider: "smtp", SMTPPassword: []byte("hunter2"),
	}}}

	at, pw, err := c.openMailboxSecret(context.Background(), uuid.New(), uuid.New(), "smtp", "ciphertext")
	if err != nil {
		t.Fatalf("openMailboxSecret: %v", err)
	}
	if string(pw) != "hunter2" || at != nil {
		t.Errorf("got at=%q pw=%q, want nil/hunter2", at, pw)
	}
}

// A client with no opener at all refuses rather than panicking. This is the
// state a mis-wired worker would be in, and a nil-pointer panic in a send path
// is a worse outcome than a failed task.
func TestAClientWithNoOpenerFailsClosed(t *testing.T) {
	var c client // zero value: creds is a nil interface

	_, _, err := c.openMailboxSecret(context.Background(), uuid.New(), uuid.New(), "smtp", "ciphertext")
	if !errors.Is(err, credbroker.ErrNotConfigured) {
		t.Fatalf("err = %v, want credbroker.ErrNotConfigured", err)
	}
}

// The keyring-backed opener with a NIL keyring is the fleet worker's local
// fallback. It must refuse, not dereference nil.
func TestALocalOpenerWithNoKeyringFailsClosed(t *testing.T) {
	o := NewCredentialOpener(nil, nil, mail.GoogleOAuth{}, mail.MicrosoftOAuth{})

	if _, err := o.OpenMailbox(context.Background(), credbroker.MailboxRef{
		WorkspaceID: uuid.New(), MailboxID: uuid.New(), Provider: "smtp", Sealed: "ct",
	}); !errors.Is(err, credbroker.ErrNotConfigured) {
		t.Errorf("OpenMailbox err = %v, want ErrNotConfigured", err)
	}
	if _, err := o.OpenWebhookEndpointSecret(context.Background(), uuid.New(), uuid.New(), []byte("ct")); !errors.Is(err, credbroker.ErrNotConfigured) {
		t.Errorf("OpenWebhookEndpointSecret err = %v, want ErrNotConfigured", err)
	}
}

// WithCredentialBroker must not be able to silently DISABLE credentials: a nil
// opener passed by a mis-wired root would otherwise leave the client unable to
// open anything while looking configured.
func TestWithCredentialBrokerIgnoresNil(t *testing.T) {
	c := client{creds: stubOpener{sec: credbroker.MailboxSecret{Provider: "smtp"}}}
	WithCredentialBroker(nil)(&c)
	if c.creds == nil {
		t.Fatal("a nil broker replaced the existing opener")
	}
}
