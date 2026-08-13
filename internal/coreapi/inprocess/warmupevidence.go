package inprocess

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// RecordWarmupTokenFailure retains an unauthenticated verifier failure without
// attributing the sender claim embedded in the untrusted token.
func (c client) RecordWarmupTokenFailure(ctx context.Context, workspaceID, recipientMailbox, fingerprint, reasonCode string) error {
	ws, err := uuid.Parse(workspaceID)
	if err != nil {
		return err
	}
	recipient, err := uuid.Parse(recipientMailbox)
	if err != nil {
		return err
	}
	return c.q.RecordWarmupTokenFailureObservation(ctx, gen.RecordWarmupTokenFailureObservationParams{
		WorkspaceID: ws, RecipientMailbox: recipient,
		ReasonCode: reasonCode,
	})
}

// RecordWarmupHardBounce attributes a DSN through a sent warmup message ID AND the
// mailbox that observed it. observerMailbox must be the mailbox whose inbox the DSN
// arrived in: Original-Message-ID is attacker-controlled, so the message id alone is
// not proof of anything. See the query for why the binding costs no true positives.
func (c client) RecordWarmupHardBounce(ctx context.Context, workspaceID, messageID, observerMailbox string) (bool, error) {
	if strings.TrimSpace(messageID) == "" {
		return false, nil
	}
	ws, err := uuid.Parse(workspaceID)
	if err != nil {
		return false, err
	}
	observer, err := uuid.Parse(observerMailbox)
	if err != nil {
		return false, err
	}
	row, err := c.q.RecordWarmupHardBounceObservation(ctx, gen.RecordWarmupHardBounceObservationParams{
		WorkspaceID: ws, MessageID: messageID, ObserverMailbox: observer,
	})
	if err != nil {
		return false, err
	}
	return row.Matched, nil
}
