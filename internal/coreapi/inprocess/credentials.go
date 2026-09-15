package inprocess

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"golang.org/x/oauth2"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/credbroker"
	"github.com/inroad/inroad/internal/platform/crypto"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/mail"
)

// localOpener is the KEYRING-BACKED credbroker.Opener: the one place a stored
// secret is actually unsealed, and the one place an OAuth token is refreshed,
// re-sealed and persisted (docs/security.md invariants 8/9).
//
// Every coreapi job build that needs a credential goes through this type, so
// swapping it for credbroker's HTTP client at a composition root moves ALL
// credential handling out of that process at once — there is no second path to
// forget. cmd/inroad always builds one of these; cmd/worker builds one only in
// the single-process self-host topology (RoleAll) and uses the remote broker
// otherwise.
//
// A nil keyring is a valid, fail-closed state: every method returns
// credbroker.ErrNotConfigured rather than panicking, so a mis-wired process
// refuses to send instead of crashing a send path. (The composition roots also
// refuse to start in that state; this is the second line.)
type localOpener struct {
	q       *gen.Queries
	keyring *crypto.Keyring
	// googleOAuth / msOAuth are the app's OAuth client configs, used to refresh
	// a gmail / m365 mailbox's access token at job-build time. A zero value
	// disables that provider: its jobs then fail cleanly rather than dialing
	// with an expired token.
	googleOAuth mail.GoogleOAuth
	msOAuth     mail.MicrosoftOAuth
}

var _ credbroker.Opener = localOpener{}

// NewCredentialOpener returns the control plane's keyring-backed credential
// opener. cmd/inroad wires one into the fleet credential-broker HTTP handler so
// a worker can obtain a decrypted transport WITHOUT holding INROAD_MASTER_KEY;
// the same constructor backs the in-process path, so the brokered and local
// answers are produced by identical code rather than two implementations that
// can drift.
func NewCredentialOpener(q *gen.Queries, keyring *crypto.Keyring, googleOAuth mail.GoogleOAuth, msOAuth mail.MicrosoftOAuth) credbroker.Opener {
	return localOpener{q: q, keyring: keyring, googleOAuth: googleOAuth, msOAuth: msOAuth}
}

// OpenMailbox returns the mailbox's decrypted transport credential: a refreshed
// short-lived access token for an API provider (gmail, m365), or the unsealed
// stored password for smtp.
//
// ref.Provider/ref.Sealed are a cache of a row the caller already read. When
// they are EMPTY the row is re-read here, workspace-pinned — which is the path
// the HTTP broker takes, because a worker must never be able to name the
// ciphertext that gets opened.
func (o localOpener) OpenMailbox(ctx context.Context, ref credbroker.MailboxRef) (credbroker.MailboxSecret, error) {
	if o.keyring == nil {
		return credbroker.MailboxSecret{}, credbroker.ErrNotConfigured
	}
	provider, sealed := ref.Provider, ref.Sealed
	if provider == "" || sealed == "" {
		if o.q == nil {
			return credbroker.MailboxSecret{}, credbroker.ErrNotConfigured
		}
		m, err := o.q.GetMailbox(ctx, gen.GetMailboxParams{ID: ref.MailboxID, WorkspaceID: ref.WorkspaceID})
		if err != nil {
			return credbroker.MailboxSecret{}, err
		}
		// Belt-and-braces on the SQL pin (invariant 4 / coreapi.ErrCrossTenant).
		if m.WorkspaceID != ref.WorkspaceID {
			return credbroker.MailboxSecret{}, coreapi.ErrCrossTenant
		}
		provider, sealed = m.Provider, m.SecretCiphertext
	}

	if provider == "gmail" || provider == "m365" {
		at, err := o.oauthAccessToken(ctx, provider, ref.MailboxID, ref.WorkspaceID, sealed, o.oauthConfigFor(provider))
		if err != nil {
			return credbroker.MailboxSecret{}, err
		}
		return credbroker.MailboxSecret{Provider: provider, AccessToken: []byte(at)}, nil
	}

	sealer, err := o.keyring.SealerFor(ctx, ref.WorkspaceID)
	if err != nil {
		return credbroker.MailboxSecret{}, err
	}
	password, err := sealer.Open(sealed)
	if err != nil {
		return credbroker.MailboxSecret{}, err
	}
	return credbroker.MailboxSecret{Provider: provider, SMTPPassword: password}, nil
}

// OpenWebhookEndpointSecret unseals an endpoint's HMAC signing secret,
// workspace-pinned. An empty sealed argument re-reads the endpoint row, the
// same cache-or-re-read rule OpenMailbox follows and for the same reason.
func (o localOpener) OpenWebhookEndpointSecret(ctx context.Context, workspaceID, endpointID uuid.UUID, sealed []byte) ([]byte, error) {
	if o.keyring == nil {
		return nil, credbroker.ErrNotConfigured
	}
	if len(sealed) == 0 {
		if o.q == nil {
			return nil, credbroker.ErrNotConfigured
		}
		ep, err := o.q.GetWebhookEndpoint(ctx, gen.GetWebhookEndpointParams{WorkspaceID: workspaceID, ID: endpointID})
		if err != nil {
			return nil, err
		}
		if ep.WorkspaceID != workspaceID {
			return nil, coreapi.ErrCrossTenant
		}
		sealed = ep.SecretCiphertext
	}
	sealer, err := o.keyring.SealerFor(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	return sealer.Open(string(sealed))
}

// oauthConfigFor returns the provider's oauth2 config for a token refresh, or
// nil when that API provider is not configured (so oauthAccessToken fails
// cleanly). Non-API providers (smtp) have no config and never reach here.
func (o localOpener) oauthConfigFor(provider string) *oauth2.Config {
	switch provider {
	case "gmail":
		if o.googleOAuth.Enabled() {
			return o.googleOAuth.Config()
		}
	case "m365":
		if o.msOAuth.Enabled() {
			return o.msOAuth.Config()
		}
	}
	return nil
}

// oauthAccessToken unseals the mailbox's OAuth token, refreshes it if expired
// (persisting the rotated token so the next build reuses it), and returns a
// valid short-lived access token. The caller never sees the refresh token —
// only the access token, which the worker zeroizes after one API call. All
// secret material and persistence stay with whoever holds the keyring, which
// after credential brokering is only ever the control plane.
//
// cfg is the provider's oauth2 config (Google for gmail, Azure AD for m365),
// resolved by the caller from the mailbox provider. A nil cfg means that
// provider is not configured; the job fails cleanly with an error the caller
// logs and does not retry into a hot loop. provider (e.g. "gmail"/"m365") is
// threaded into the error/log context so a refresh or persist failure is
// triagable to the right transport; it is never a secret.
func (o localOpener) oauthAccessToken(ctx context.Context, provider string, mailboxID, workspaceID uuid.UUID, sealed string, cfg *oauth2.Config) (string, error) {
	if cfg == nil {
		return "", fmt.Errorf("%s oauth not configured", provider)
	}
	// One workspace-bound Sealer for both the initial Open and the reseal below,
	// so a rotated token re-seals under the SAME per-workspace DEK it opened.
	sealer, err := o.keyring.SealerFor(ctx, workspaceID)
	if err != nil {
		return "", err
	}
	raw, err := sealer.Open(sealed)
	if err != nil {
		return "", err
	}
	tok, err := mail.UnmarshalToken(raw)
	if err != nil {
		return "", err
	}
	// TokenSource is a ReuseTokenSource: it refreshes via the refresh token only
	// when the access token has expired, so we don't hit the provider every build.
	ts := cfg.TokenSource(ctx, tok)
	fresh, err := ts.Token()
	if err != nil {
		return "", fmt.Errorf("%s oauth token refresh: %w", provider, err)
	}
	// Persist only on change (access token refreshed, or the provider rotated the
	// refresh token) so a rotated refresh token isn't silently lost. A failed
	// re-seal/persist is non-fatal: the returned access token is still valid for
	// this send, and the next build retries the refresh — but we log a warning so
	// a lost rotation is observable. Log the mailbox id ONLY, never the
	// token/ciphertext.
	if fresh.AccessToken != tok.AccessToken || fresh.RefreshToken != tok.RefreshToken {
		if b, err := mail.MarshalToken(fresh); err == nil {
			if ct, err := sealer.Seal(b); err == nil {
				if err := o.q.UpdateMailboxSecret(ctx, gen.UpdateMailboxSecretParams{
					ID: mailboxID, WorkspaceID: workspaceID, SecretCiphertext: ct,
				}); err != nil {
					slog.Warn("oauth token reseal persist failed", "provider", provider, "mailbox", mailboxID, "err", err)
				}
			}
		}
	}
	return fresh.AccessToken, nil
}

// openMailboxSecret is the single call every coreapi job build makes to turn a
// mailbox row into a usable credential. It replaces the five identical
// provider-branch blocks that used to sit inline in GetStepSendJob,
// GetInboxPollJob, GetWarmupSendJob, GetWarmupEngageJob and
// ResolveSenderTransport — identical code five times is five places for a
// credential-handling rule to be applied unevenly.
//
// The returned pair is (accessToken, password): exactly one is non-nil, chosen
// by the mailbox's provider. Both are []byte the worker zeroizes after use.
func (c client) openMailboxSecret(ctx context.Context, ws, mailboxID uuid.UUID, provider, sealed string) (accessToken, password []byte, err error) {
	if c.creds == nil {
		return nil, nil, credbroker.ErrNotConfigured
	}
	sec, err := c.creds.OpenMailbox(ctx, credbroker.MailboxRef{
		WorkspaceID: ws, MailboxID: mailboxID, Provider: provider, Sealed: sealed,
	})
	if err != nil {
		return nil, nil, err
	}
	return sec.AccessToken, sec.SMTPPassword, nil
}
