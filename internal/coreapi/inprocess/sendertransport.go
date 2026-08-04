package inprocess

import (
	"context"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/campaign"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/mail"
)

// ResolveSenderTransport resolves one mailbox's decrypted send transport for a
// control-plane test-send (POST /campaigns/{id}/test-send). It is NOT part of
// coreapi.Client (see coreapi.BreakerResult's doc comment for why widening
// that ~40-method interface for one call site is the wrong trade): the
// campaign package consumes it through its own one-method
// campaign.SenderResolver interface, which this satisfies by type assertion
// at the composition root (the same maintenance.Cleaner / deliverability.
// Breaker pattern).
//
// Decrypting the credential — and, for a gmail/m365 mailbox, refreshing the
// OAuth access token — reuses the SAME oauthAccessToken/keyring path every
// worker send job uses (GetSendJob, GetStepSendJob), so there is exactly ONE
// implementation of "how a mailbox's credential is opened", never a second
// copy for the control-plane test-send path (security invariants 8/9).
func (c client) ResolveSenderTransport(ctx context.Context, ws, mailboxID uuid.UUID) (campaign.SenderTransport, error) {
	m, err := c.q.GetMailbox(ctx, gen.GetMailboxParams{ID: mailboxID, WorkspaceID: ws})
	if err != nil {
		return campaign.SenderTransport{}, err
	}

	var accessToken, password []byte
	if m.Provider == "gmail" || m.Provider == "m365" {
		at, err := c.oauthAccessToken(ctx, m.Provider, m.ID, ws, m.SecretCiphertext, c.oauthConfigFor(m.Provider))
		if err != nil {
			return campaign.SenderTransport{}, err
		}
		accessToken = []byte(at)
	} else {
		sealer, serr := c.keyring.SealerFor(ctx, ws)
		if serr != nil {
			return campaign.SenderTransport{}, serr
		}
		password, err = sealer.Open(m.SecretCiphertext)
		if err != nil {
			return campaign.SenderTransport{}, err
		}
	}

	return campaign.SenderTransport{
		FromEmail: m.Email, FromName: m.DisplayName,
		OutboundJob: mail.OutboundJob{
			Provider: m.Provider, Host: m.SmtpHost, Port: int(m.SmtpPort),
			Username: m.SmtpUsername, Password: string(password),
			AllowPlaintext: m.AllowPlaintext, AccessToken: string(accessToken),
		},
	}, nil
}
