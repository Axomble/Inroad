package inprocess

import (
	"context"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// localResolveSenderTransport resolves one mailbox's decrypted send transport for
// the testsend:send task (internal/worker/testsend). It is NOT part of
// coreapi.Client (see coreapi.SenderTransport's doc comment for why widening
// that ~40-method interface for one call site is the wrong trade): the
// testsend worker consumes it through its own narrow testsend.Core interface,
// which this satisfies by type assertion at the composition root (the same
// maintenance.Cleaner / deliverability.Breaker pattern).
//
// Decrypting the credential — and, for a gmail/m365 mailbox, refreshing the
// OAuth access token — goes through c.openMailboxSecret, the SAME credential
// opener every other job build uses (GetStepSendJob, GetWarmupSendJob), so
// there is exactly ONE implementation of "how a mailbox's credential is opened"
// (security invariants 8/9). On a fleet worker that opener is an HTTP call to
// the control plane, so the unsealing itself happens where the key is.
func (c client) localResolveSenderTransport(ctx context.Context, workspaceID, mailboxID string) (coreapi.SenderTransport, error) {
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

	accessToken, password, err := c.openMailboxSecret(ctx, ws, m.ID, m.Provider, m.SecretCiphertext)
	if err != nil {
		return coreapi.SenderTransport{}, err
	}

	return coreapi.SenderTransport{
		FromEmail: m.Email, FromName: m.DisplayName, Provider: m.Provider,
		AccessToken: accessToken, SMTPHost: m.SmtpHost, SMTPPort: int(m.SmtpPort),
		SMTPUsername: m.SmtpUsername, SMTPPassword: password, AllowPlaintext: m.AllowPlaintext,
	}, nil
}
