package inprocess

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"golang.org/x/oauth2"

	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/mail"
)

// oauthAccessToken unseals the mailbox's OAuth token, refreshes it if expired
// (persisting the rotated token so the next build reuses it), and returns a
// valid short-lived access token. The worker never sees the refresh token —
// only the access token, which it zeroizes after one API call. All secret
// material and persistence stay in the control plane.
//
// cfg is the provider's oauth2 config (Google for gmail, Azure AD for m365),
// resolved by the caller from the mailbox provider. A nil cfg means that
// provider is not configured; the job fails cleanly with an error the caller
// logs and does not retry into a hot loop.
func (c client) oauthAccessToken(ctx context.Context, mailboxID, workspaceID uuid.UUID, sealed string, cfg *oauth2.Config) (string, error) {
	if cfg == nil {
		return "", fmt.Errorf("oauth not configured")
	}
	raw, err := c.sealer.Open(sealed)
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
		return "", fmt.Errorf("oauth token refresh: %w", err)
	}
	// Persist only on change (access token refreshed, or the provider rotated the
	// refresh token) so a rotated refresh token isn't silently lost. A failed
	// re-seal/persist is non-fatal: the returned access token is still valid for
	// this send, and the next build retries the refresh — but we log a warning so
	// a lost rotation is observable. Log the mailbox id ONLY, never the
	// token/ciphertext.
	if fresh.AccessToken != tok.AccessToken || fresh.RefreshToken != tok.RefreshToken {
		if b, err := mail.MarshalToken(fresh); err == nil {
			if ct, err := c.sealer.Seal(b); err == nil {
				if err := c.q.UpdateMailboxSecret(ctx, gen.UpdateMailboxSecretParams{
					ID: mailboxID, WorkspaceID: workspaceID, SecretCiphertext: ct,
				}); err != nil {
					slog.Warn("oauth token reseal persist failed", "mailbox", mailboxID, "err", err)
				}
			}
		}
	}
	return fresh.AccessToken, nil
}
