// Package credbroker is the seam through which a process obtains an OPENED
// (decrypted) stored credential, and the two transports that satisfy it.
//
// Why it exists. Every stored secret is sealed under a per-workspace DEK which
// is itself wrapped by the KEK derived from INROAD_MASTER_KEY
// (docs/security.md invariants 14–17). Opening one therefore requires the
// master key, and until this package existed the ONLY way to open one was to
// hold that key — which is why cmd/worker built a full crypto.Keyring and every
// compose file handed a worker host INROAD_MASTER_KEY. A worker host could
// unwrap any workspace's DEK and read every stored SMTP password and OAuth
// refresh token in the installation, offline and forever.
//
// This package splits "open a credential" from "hold the key that can open
// anything". A control-plane process implements Opener locally over its keyring
// and pool; an execution-plane process implements it as an authenticated HTTP
// call to the control plane and holds no key at all.
//
// What it is NOT. Brokering does not stop a worker from holding a usable
// credential at the moment it dials — a process that authenticates to a
// customer's mailbox must have something to authenticate with, and that is
// inherent rather than a flaw. What it removes is the OFFLINE, permanent,
// un-revocable capability: a stolen worker disk or environment now yields no
// key, and the control plane can revoke a fleet's access by rotating one token
// instead of re-encrypting every DEK in the installation.
//
// Layering: this is platform, so both composition roots (cmd/inroad,
// cmd/worker) and the coreapi implementation may import it. It imports no
// app/* or coreapi package — the LOCAL implementation lives in
// coreapi/inprocess, which already holds the pool, the keyring and the OAuth
// configs, so there is exactly one implementation of "how a mailbox credential
// is opened" (the property security invariants 8/9 rest on).
package credbroker

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

// ErrNotConfigured is returned by an Opener that cannot open anything because
// its process was given neither a key nor a broker. It exists so the failure is
// a clean, non-retryable refusal to send rather than a nil-pointer panic: a
// worker that cannot obtain a credential must refuse, never fall back to
// something weaker.
var ErrNotConfigured = errors.New("credbroker: no credential source configured")

// MailboxRef names the mailbox whose stored credential is to be opened.
type MailboxRef struct {
	WorkspaceID uuid.UUID
	MailboxID   uuid.UUID

	// Provider and Sealed are a CACHE of the mailbox row the caller has already
	// read, not authority. The LOCAL opener uses them so a job build that just
	// selected the row does not select it again — that keeps the single-process
	// self-host topology at exactly the query count it had before this package.
	//
	// The REMOTE opener ignores both and sends only the two ids: if a worker
	// could name the ciphertext to open, the broker would be a general-purpose
	// decryption oracle and moving the key would have bought nothing. The
	// control plane re-reads the row itself, workspace-pinned.
	Provider string
	Sealed   string
}

// MailboxSecret is one mailbox's opened transport credential. Exactly one of
// the two secret fields is populated, selected by Provider: an API provider
// (gmail, m365) yields a short-lived access token that the opener has already
// refreshed and re-sealed if needed; smtp yields the stored password.
//
// It deliberately carries no refresh token. The worker never receives one
// (docs/security.md invariant 8) — refresh, reseal and persist stay with
// whoever holds the key (invariant 9), which after brokering is only ever the
// control plane.
type MailboxSecret struct {
	Provider     string
	AccessToken  []byte
	SMTPPassword []byte
}

// Opener opens a stored credential for one named subject. It is deliberately
// two narrow methods rather than a general "decrypt this blob": every method
// names WHAT is being opened, so a remote implementation can re-derive the
// ciphertext from its own database instead of trusting the caller's.
type Opener interface {
	// OpenMailbox returns the mailbox's decrypted transport credential.
	OpenMailbox(ctx context.Context, ref MailboxRef) (MailboxSecret, error)

	// OpenWebhookEndpointSecret returns the endpoint's HMAC signing secret,
	// workspace-pinned. sealed is the same kind of cache as MailboxRef.Sealed:
	// used locally, ignored remotely.
	OpenWebhookEndpointSecret(ctx context.Context, workspaceID, endpointID uuid.UUID, sealed []byte) ([]byte, error)
}

// Unconfigured is the Opener a process gets when it holds neither a key nor a
// broker. Every call fails closed with ErrNotConfigured.
//
// It is the zero-configuration default rather than a nil interface so that a
// mis-wired composition root produces a logged, retried task failure with a
// readable cause instead of a panic in a send path. The composition roots still
// refuse to START in that state (see cmd/worker); this is the second line.
type Unconfigured struct{}

var _ Opener = Unconfigured{}

// OpenMailbox always fails closed.
func (Unconfigured) OpenMailbox(context.Context, MailboxRef) (MailboxSecret, error) {
	return MailboxSecret{}, ErrNotConfigured
}

// OpenWebhookEndpointSecret always fails closed.
func (Unconfigured) OpenWebhookEndpointSecret(context.Context, uuid.UUID, uuid.UUID, []byte) ([]byte, error) {
	return nil, ErrNotConfigured
}
