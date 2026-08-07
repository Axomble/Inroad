package inprocess

import (
	"context"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// ResolveSenderTransport resolves one mailbox's decrypted send transport for
// the testsend:send task (internal/worker/testsend). It is NOT part of
// coreapi.Client (see coreapi.SenderTransport's doc comment for why widening
// that ~40-method interface for one call site is the wrong trade): the
// testsend worker consumes it through its own narrow testsend.Core interface,
// which this satisfies by type assertion at the composition root (the same
// maintenance.Cleaner / deliverability.Breaker pattern).
//
// Decrypting the credential — and, for a gmail/m365 mailbox, refreshing the
// OAuth access token — reuses the SAME oauthAccessToken/keyring path every
// other worker send job uses (GetStepSendJob, GetWarmupSendJob), so there is exactly
// ONE implementation of "how a mailbox's credential is opened" (security
// invariants 8/9). This runs ONLY in the execution plane (cmd/worker), never
// in cmd/inroad (docs/security.md invariant 1).
func (c client) ResolveSenderTransport(ctx context.Context, workspaceID, mailboxID string) (coreapi.SenderTransport, error) {
	ws, err := uuid.Parse(workspaceID)
	if err != nil {
		return coreapi.SenderTransport{}, err
	}
	mid, err := uuid.Parse(mailboxID)
	if err != nil {
		return coreapi.SenderTransport{}, err
	}

	m, err := c.q.GetMailbox(ctx, gen.GetMailboxParams{ID: mid, WorkspaceID: ws})
	if err != nil {
		return coreapi.SenderTransport{}, err
	}

	var accessToken, password []byte
	if m.Provider == "gmail" || m.Provider == "m365" {
		at, aerr := c.oauthAccessToken(ctx, m.Provider, m.ID, ws, m.SecretCiphertext, c.oauthConfigFor(m.Provider))
		if aerr != nil {
			return coreapi.SenderTransport{}, aerr
		}
		accessToken = []byte(at)
	} else {
		sealer, serr := c.keyring.SealerFor(ctx, ws)
		if serr != nil {
			return coreapi.SenderTransport{}, serr
		}
		password, err = sealer.Open(m.SecretCiphertext)
		if err != nil {
			return coreapi.SenderTransport{}, err
		}
	}

	return coreapi.SenderTransport{
		FromEmail: m.Email, FromName: m.DisplayName, Provider: m.Provider,
		AccessToken: accessToken, SMTPHost: m.SmtpHost, SMTPPort: int(m.SmtpPort),
		SMTPUsername: m.SmtpUsername, SMTPPassword: password, AllowPlaintext: m.AllowPlaintext,
	}, nil
}
