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
		Fingerprint: fingerprint, ReasonCode: reasonCode,
	})
}

// RecordWarmupHardBounce attributes a DSN only through a sent warmup message ID.
func (c client) RecordWarmupHardBounce(ctx context.Context, workspaceID, messageID string) (bool, error) {
	if strings.TrimSpace(messageID) == "" {
		return false, nil
	}
	ws, err := uuid.Parse(workspaceID)
	if err != nil {
		return false, err
	}
	row, err := c.q.RecordWarmupHardBounceObservation(ctx, gen.RecordWarmupHardBounceObservationParams{
		WorkspaceID: ws, MessageID: messageID,
	})
	if err != nil {
		return false, err
	}
	return row.Matched, nil
}
