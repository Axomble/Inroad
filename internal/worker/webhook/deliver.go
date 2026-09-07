// Package webhook is the execution-plane handler for webhook:deliver: it loads a
// queued delivery through coreapi, re-runs the SSRF guard on the receiver URL,
// signs the stored body, and POSTs it — retrying on the app-level backoff
// schedule until it succeeds or the schedule is exhausted.
package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/queue"
	"github.com/inroad/inroad/internal/platform/webhookwire"
)

// postTimeout bounds one delivery POST. Only a 2xx is success; 3xx is not
// followed.
const postTimeout = 10 * time.Second

// maxAttempts is the app-level retry ceiling: 1 initial + 5 backoff retries.
const maxAttempts = 6

// backoff[N-1] is the delay before attempt N+1 after attempt N failed. The last
// value is reused (clamped) for any attempt beyond it.
var backoff = []time.Duration{
	1 * time.Minute,
	5 * time.Minute,
	30 * time.Minute,
	2 * time.Hour,
	6 * time.Hour,
}

// Core is the narrow control-plane capability this handler needs, defined by the
// consumer and satisfied by the in-process coreapi client via type assertion (so
// coreapi.Client and its fakes are not widened for one call site).
type Core interface {
	GetWebhookDeliveryJob(ctx context.Context, deliveryID, workspaceID string) (coreapi.WebhookDeliveryJob, error)
	MarkWebhookDelivered(ctx context.Context, deliveryID, workspaceID string, attempts, responseStatus int) error
	MarkWebhookRetrying(ctx context.Context, deliveryID, workspaceID string, attempts int, lastErr string, responseStatus *int, nextAttemptAt time.Time) error
	MarkWebhookFailed(ctx context.Context, deliveryID, workspaceID string, attempts int, lastErr string, responseStatus *int) error
}

// Enqueuer is the queue seam for the backoff re-enqueue. queue.Client satisfies it.
type Enqueuer interface {
	EnqueueWebhookDeliverIn(deliveryID, workspaceID string, d time.Duration) error
}

// DeliverHandler builds the webhook:deliver task handler. allowPrivate mirrors
// INROAD_WEBHOOK_ALLOW_PRIVATE: it relaxes the SSRF guard's loopback/private
// check (never the always-hostile ranges) for local dev.
func DeliverHandler(core Core, enq Enqueuer, allowPrivate bool) func(context.Context, *asynq.Task) error {
	client := &http.Client{
		Timeout: postTimeout,
		// Do not follow redirects: a 3xx is not a delivery success, and following
		// one would let a receiver bounce the signed body to an arbitrary URL.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{
			// Resolve + re-classify + dial the vetted IP, closing the
			// DNS-rebinding window between the VetURL check below and the dial.
			DialContext:           mail.GuardedDialContext(allowPrivate),
			TLSHandshakeTimeout:   postTimeout,
			ResponseHeaderTimeout: postTimeout,
			MaxIdleConns:          10,
		},
	}
	return func(ctx context.Context, t *asynq.Task) error {
		var p queue.WebhookDeliverPayload
		if err := json.Unmarshal(t.Payload(), &p); err != nil {
			// A malformed payload can never be retried into a good one.
			return fmt.Errorf("webhook deliver: bad payload: %w", asynq.SkipRetry)
		}
		return deliver(ctx, core, enq, client, allowPrivate, p)
	}
}

func deliver(ctx context.Context, core Core, enq Enqueuer, client *http.Client, allowPrivate bool, p queue.WebhookDeliverPayload) error {
	job, err := core.GetWebhookDeliveryJob(ctx, p.DeliveryID, p.WorkspaceID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// The delivery (or its endpoint) was deleted after the task was
			// queued — nothing to do, and retrying will never find it.
			slog.InfoContext(ctx, "webhook delivery no longer exists", "delivery_id", p.DeliveryID)
			return nil
		}
		return fmt.Errorf("webhook deliver: load job: %w", err)
	}
	// Zeroize the decrypted secret once we are done with it, like every other
	// job's credential.
	defer wipe(job.Secret)

	// A retried asynq job that races a finalize, or one for an already-terminal
	// row: the DB guard would no-op anyway, but skip the POST entirely.
	if job.Status != "pending" {
		return nil
	}
	if !job.EndpointActive {
		return core.MarkWebhookFailed(ctx, job.DeliveryID, job.WorkspaceID, job.Attempts, "endpoint is inactive", nil)
	}

	// Re-run the SSRF guard immediately before dialing (DNS rebinding). A block
	// here is terminal: a URL that now resolves internal is not going to become
	// safe on a retry.
	if _, err := webhookwire.VetURL(ctx, job.URL, allowPrivate); err != nil {
		return core.MarkWebhookFailed(ctx, job.DeliveryID, job.WorkspaceID, job.Attempts+1, "blocked by SSRF guard: "+err.Error(), nil)
	}

	attempts := job.Attempts + 1
	status, transportErr := post(ctx, client, job)

	if transportErr == nil && status >= 200 && status < 300 {
		return core.MarkWebhookDelivered(ctx, job.DeliveryID, job.WorkspaceID, attempts, status)
	}

	// Failure. Build the diagnostic and decide retry vs give up.
	var respStatus *int
	lastErr := ""
	if transportErr != nil {
		lastErr = "transport: " + transportErr.Error()
	} else {
		respStatus = &status
		lastErr = fmt.Sprintf("receiver returned HTTP %d", status)
	}

	if attempts >= maxAttempts {
		return core.MarkWebhookFailed(ctx, job.DeliveryID, job.WorkspaceID, attempts, lastErr, respStatus)
	}
	delay := backoff[min(attempts-1, len(backoff)-1)]
	if err := core.MarkWebhookRetrying(ctx, job.DeliveryID, job.WorkspaceID, attempts, lastErr, respStatus, time.Now().Add(delay)); err != nil {
		return fmt.Errorf("webhook deliver: mark retrying: %w", err)
	}
	if err := enq.EnqueueWebhookDeliverIn(job.DeliveryID, job.WorkspaceID, delay); err != nil {
		return fmt.Errorf("webhook deliver: re-enqueue: %w", err)
	}
	return nil
}

// post sends one delivery POST and returns the receiver's status code (0 on a
// transport-level failure, with the error).
func post(ctx context.Context, client *http.Client, job coreapi.WebhookDeliveryJob) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, job.URL, bytes.NewReader(job.Payload))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Inroad-Webhooks/1")
	req.Header.Set("Inroad-Event", job.EventType)
	req.Header.Set("Inroad-Delivery-Id", job.DeliveryID)
	req.Header.Set("Inroad-Webhook-Id", job.EndpointID)
	req.Header.Set(webhookwire.SignatureHeader, webhookwire.Sign(job.Secret, job.Payload, time.Now()))

	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	// Drain a bounded amount so the connection can be reused; the body is not
	// stored.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode, nil
}

func wipe(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
