package inprocess

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// GetWebhookDeliveryJob loads one webhook delivery plus its endpoint's URL and
// sealed secret, workspace-pinned, and opens the secret under the workspace DEK
// so the worker receives plaintext bytes it can sign with and zeroize. The
// worker never touches the keyring (docs/security.md invariant 1).
//
// Consumed through internal/worker/webhook.Core, not coreapi.Client — see the
// WebhookDeliveryJob doc for why.
func (c client) GetWebhookDeliveryJob(ctx context.Context, deliveryID, workspaceID string) (coreapi.WebhookDeliveryJob, error) {
	did, err := uuid.Parse(deliveryID)
	if err != nil {
		return coreapi.WebhookDeliveryJob{}, err
	}
	ws, err := uuid.Parse(workspaceID)
	if err != nil {
		return coreapi.WebhookDeliveryJob{}, err
	}
	row, err := c.q.GetWebhookDeliveryForSend(ctx, gen.GetWebhookDeliveryForSendParams{WorkspaceID: ws, ID: did})
	if err != nil {
		return coreapi.WebhookDeliveryJob{}, err
	}
	// Belt-and-braces on the SQL WHERE pin (invariant 4 / coreapi.ErrCrossTenant).
	if row.WorkspaceID != ws {
		return coreapi.WebhookDeliveryJob{}, coreapi.ErrCrossTenant
	}

	sealer, err := c.keyring.SealerFor(ctx, ws)
	if err != nil {
		return coreapi.WebhookDeliveryJob{}, err
	}
	secret, err := sealer.Open(string(row.EndpointSecretCiphertext))
	if err != nil {
		return coreapi.WebhookDeliveryJob{}, err
	}
	return coreapi.WebhookDeliveryJob{
		DeliveryID:     row.DeliveryID.String(),
		EndpointID:     row.EndpointID.String(),
		WorkspaceID:    row.WorkspaceID.String(),
		EventType:      row.EventType,
		Payload:        row.Payload,
		Secret:         secret,
		URL:            row.EndpointUrl,
		Attempts:       int(row.Attempts),
		Status:         row.Status,
		EndpointActive: row.EndpointActive,
	}, nil
}

// MarkWebhookDelivered finalizes a delivery to 'delivered' with the receiver's
// 2xx status and the attempt count. The UPDATE is guarded on status='pending',
// so a retried asynq job that races a finalize is a clean no-op.
func (c client) MarkWebhookDelivered(ctx context.Context, deliveryID, workspaceID string, attempts, responseStatus int) error {
	did, ws, err := parseDeliveryIDs(deliveryID, workspaceID)
	if err != nil {
		return err
	}
	_, err = c.q.MarkWebhookDeliveryDelivered(ctx, gen.MarkWebhookDeliveryDeliveredParams{
		WorkspaceID:    ws,
		ID:             did,
		Attempts:       int32(attempts),
		ResponseStatus: int32(responseStatus),
	})
	return err
}

// MarkWebhookRetrying records a failed attempt that still has retries left: the
// row stays 'pending' and next_attempt_at carries the backoff schedule's next
// due time. responseStatus is nil for a transport-level failure (no HTTP
// response).
func (c client) MarkWebhookRetrying(ctx context.Context, deliveryID, workspaceID string, attempts int, lastErr string, responseStatus *int, nextAttemptAt time.Time) error {
	did, ws, err := parseDeliveryIDs(deliveryID, workspaceID)
	if err != nil {
		return err
	}
	_, err = c.q.MarkWebhookDeliveryRetrying(ctx, gen.MarkWebhookDeliveryRetryingParams{
		WorkspaceID:    ws,
		ID:             did,
		Attempts:       int32(attempts),
		LastError:      truncateError(lastErr),
		ResponseStatus: int32Ptr(responseStatus),
		NextAttemptAt:  pgtype.Timestamptz{Time: nextAttemptAt, Valid: true},
	})
	return err
}

// MarkWebhookFailed finalizes a delivery to 'failed' after the app-level retry
// schedule is exhausted. Same 'pending' guard as the success path.
func (c client) MarkWebhookFailed(ctx context.Context, deliveryID, workspaceID string, attempts int, lastErr string, responseStatus *int) error {
	did, ws, err := parseDeliveryIDs(deliveryID, workspaceID)
	if err != nil {
		return err
	}
	_, err = c.q.MarkWebhookDeliveryFailed(ctx, gen.MarkWebhookDeliveryFailedParams{
		WorkspaceID:    ws,
		ID:             did,
		Attempts:       int32(attempts),
		LastError:      truncateError(lastErr),
		ResponseStatus: int32Ptr(responseStatus),
	})
	return err
}

func parseDeliveryIDs(deliveryID, workspaceID string) (uuid.UUID, uuid.UUID, error) {
	did, err := uuid.Parse(deliveryID)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	ws, err := uuid.Parse(workspaceID)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	return did, ws, nil
}

func int32Ptr(p *int) *int32 {
	if p == nil {
		return nil
	}
	v := int32(*p)
	return &v
}

// truncateError bounds the diagnostic stored on a delivery row: a receiver's
// error body can be arbitrarily large, and last_error is operator-facing
// display, not a log.
func truncateError(s string) string {
	const maxLen = 1000
	if len(s) > maxLen {
		return s[:maxLen]
	}
	return s
}
