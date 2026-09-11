// Package webhook owns outbound webhooks: the endpoints a workspace registers,
// the per-attempt delivery log, the HMAC signing of each POST, and the SSRF
// guard on receiver URLs.
//
// The control plane inserts a webhook_deliveries row and enqueues a
// webhook:deliver task (Service.Dispatch, driven by the Emitter seam); the
// execution plane (internal/worker/webhook) loads the row through coreapi,
// re-checks the URL, signs, and POSTs. The signing secret is sealed at rest
// under the per-workspace DEK and returned to the operator exactly once.
package webhook

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/inroad/inroad/internal/platform/crypto"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/webhookwire"
)

var (
	// ErrNotFound is returned when the workspace has no such endpoint. A row
	// owned by another workspace produces this too — a tenant must not be able to
	// tell a foreign id from a nonexistent one.
	ErrNotFound = errors.New("webhook: not found")
	// ErrValidation is returned for a caller-fixable input problem (bad URL,
	// unknown event type, bad cursor is separate).
	ErrValidation = errors.New("webhook: invalid")
	// ErrBadCursor is returned for a delivery-log page cursor this server did not
	// mint. Separate from ErrValidation because it maps to 400, not 422: a cursor
	// is opaque machine state, so a bad one is a malformed request rather than a
	// value the operator chose.
	ErrBadCursor = errors.New("webhook: bad cursor")
)

// Event catalog. These are the types an endpoint may subscribe to and the types
// Dispatch fans out. "ping" is deliverable (Service.Ping) but is deliberately
// NOT subscribable — it is the test-delivery event, always sent on demand.
const (
	EventReplyReceived       = "reply.received"
	EventEmailBounced        = "email.bounced"
	EventContactUnsubscribed = "contact.unsubscribed"
	EventPing                = "ping"
)

// subscribable is the closed set an endpoint's event_types entries are validated
// against. Kept as a sorted slice (not a map) so the error message can list it.
var subscribable = []string{EventContactUnsubscribed, EventEmailBounced, EventReplyReceived}

// KnownEventTypes returns the subscribable catalog, for the OpenAPI enum and any
// caller that wants to present the choices.
func KnownEventTypes() []string { return slices.Clone(subscribable) }

const (
	secretBytes       = 32
	maxURLLen         = 2048
	maxDescriptionLen = 500
	defaultPageLimit  = 50
	maxPageLimit      = 100
)

// Enqueuer is the queue seam Dispatch and Ping enqueue a delivery through — one
// method, defined here by the consumer. internal/platform/queue.Client
// satisfies it; a unit test injects a fake and needs no Redis.
type Enqueuer interface {
	EnqueueWebhookDeliver(ctx context.Context, deliveryID, workspaceID string) error
}

// Service holds this domain's business rules. It depends on the Store interface,
// the crypto.Keyring (for at-rest secret sealing under the workspace DEK), and
// the Enqueuer seam — never on a concrete store or on asynq.
type Service struct {
	store        Store
	keyring      *crypto.Keyring
	enq          Enqueuer
	allowPrivate bool
	now          func() time.Time
}

// ServiceOption configures optional collaborators.
type ServiceOption func(*Service)

// WithClock overrides the service clock (tests only).
func WithClock(now func() time.Time) ServiceOption { return func(s *Service) { s.now = now } }

// NewService builds the service. allowPrivate comes from
// INROAD_WEBHOOK_ALLOW_PRIVATE and, when true, relaxes the SSRF guard's
// loopback/private check (never the always-hostile ranges) for local dev.
func NewService(store Store, keyring *crypto.Keyring, enq Enqueuer, allowPrivate bool, opts ...ServiceOption) *Service {
	s := &Service{store: store, keyring: keyring, enq: enq, allowPrivate: allowPrivate, now: time.Now}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// CreateInput is the validated create request.
type CreateInput struct {
	URL         string
	Description string
	EventTypes  []string
}

// UpdateInput is the validated PATCH request: a nil pointer / nil EventTypes
// means "unchanged".
type UpdateInput struct {
	URL         *string
	Description *string
	EventTypes  []string
	Active      *bool
}

// List returns every endpoint in the workspace.
func (s *Service) List(ctx context.Context, ws uuid.UUID) ([]gen.WebhookEndpoint, error) {
	return s.store.ListEndpoints(ctx, ws)
}

// Get returns one endpoint, workspace-scoped.
func (s *Service) Get(ctx context.Context, ws, id uuid.UUID) (gen.WebhookEndpoint, error) {
	ep, err := s.store.GetEndpoint(ctx, ws, id)
	if err != nil {
		return gen.WebhookEndpoint{}, translateRead(err)
	}
	return ep, nil
}

// Create validates the URL (parse + SSRF) and the event-type subscription, mints
// a 32-byte signing secret, seals it under the workspace DEK, and persists the
// endpoint. The raw secret is returned to the caller HERE and never again.
func (s *Service) Create(ctx context.Context, ws uuid.UUID, in CreateInput) (gen.WebhookEndpoint, string, error) {
	normURL, err := s.vet(ctx, in.URL)
	if err != nil {
		return gen.WebhookEndpoint{}, "", err
	}
	desc, err := validateDescription(in.Description)
	if err != nil {
		return gen.WebhookEndpoint{}, "", err
	}
	types, err := validateEventTypes(in.EventTypes)
	if err != nil {
		return gen.WebhookEndpoint{}, "", err
	}

	secret, sealed, err := s.mintSecret(ctx, ws)
	if err != nil {
		return gen.WebhookEndpoint{}, "", err
	}
	ep, err := s.store.CreateEndpoint(ctx, ws, CreateParams{
		URL: normURL, Description: desc, EventTypes: types, SealedSecret: sealed,
	})
	if err != nil {
		return gen.WebhookEndpoint{}, "", err
	}
	return ep, secret, nil
}

// Update applies a PATCH. A URL that is present is re-vetted (SSRF is checked at
// create AND update); an absent one is left alone.
func (s *Service) Update(ctx context.Context, ws, id uuid.UUID, in UpdateInput) (gen.WebhookEndpoint, error) {
	params := UpdateParams{Active: in.Active}
	if in.URL != nil {
		normURL, err := s.vet(ctx, *in.URL)
		if err != nil {
			return gen.WebhookEndpoint{}, err
		}
		params.URL = &normURL
	}
	if in.Description != nil {
		desc, err := validateDescription(*in.Description)
		if err != nil {
			return gen.WebhookEndpoint{}, err
		}
		params.Description = &desc
	}
	if in.EventTypes != nil {
		types, err := validateEventTypes(in.EventTypes)
		if err != nil {
			return gen.WebhookEndpoint{}, err
		}
		params.EventTypes = types
	}
	ep, err := s.store.UpdateEndpoint(ctx, ws, id, params)
	if err != nil {
		return gen.WebhookEndpoint{}, translateRead(err)
	}
	return ep, nil
}

// RotateSecret mints a fresh signing secret, seals it, and swaps it in. The old
// secret stops verifying immediately — a receiver must be updated in step. The
// new raw secret is returned once.
func (s *Service) RotateSecret(ctx context.Context, ws, id uuid.UUID) (gen.WebhookEndpoint, string, error) {
	if _, err := s.store.GetEndpoint(ctx, ws, id); err != nil {
		return gen.WebhookEndpoint{}, "", translateRead(err)
	}
	secret, sealed, err := s.mintSecret(ctx, ws)
	if err != nil {
		return gen.WebhookEndpoint{}, "", err
	}
	ep, err := s.store.RotateEndpointSecret(ctx, ws, id, sealed)
	if err != nil {
		return gen.WebhookEndpoint{}, "", translateRead(err)
	}
	return ep, secret, nil
}

// Delete removes an endpoint (its deliveries cascade).
func (s *Service) Delete(ctx context.Context, ws, id uuid.UUID) error {
	deleted, err := s.store.DeleteEndpoint(ctx, ws, id)
	if err != nil {
		return err
	}
	if !deleted {
		return ErrNotFound
	}
	return nil
}

// Ping creates and enqueues a synthetic "ping" delivery to one endpoint,
// regardless of its subscription list, so an operator can prove a receiver is
// reachable. It returns the created delivery row.
func (s *Service) Ping(ctx context.Context, ws, id uuid.UUID) (gen.WebhookDelivery, error) {
	ep, err := s.store.GetEndpoint(ctx, ws, id)
	if err != nil {
		return gen.WebhookDelivery{}, translateRead(err)
	}
	del, err := s.enqueueDelivery(ctx, ep, EventPing, map[string]any{"message": "This is a test delivery from Inroad."}, s.now())
	if err != nil {
		return gen.WebhookDelivery{}, err
	}
	return del, nil
}

// DeliveryPage is one keyset page of an endpoint's delivery log.
type DeliveryPage struct {
	Items      []gen.WebhookDelivery
	NextCursor string
}

// ListDeliveries returns one page of an endpoint's delivery log, newest first.
// It resolves the endpoint first (workspace-pinned), so another tenant's id — or
// an unknown one — is a 404 rather than a silently empty page.
func (s *Service) ListDeliveries(ctx context.Context, ws, endpointID uuid.UUID, rawCursor string, limit int32) (DeliveryPage, error) {
	if _, err := s.store.GetEndpoint(ctx, ws, endpointID); err != nil {
		return DeliveryPage{}, translateRead(err)
	}
	if limit <= 0 {
		limit = defaultPageLimit
	}
	if limit > maxPageLimit {
		limit = maxPageLimit
	}

	q := DeliveryQuery{Limit: limit + 1} // one-row lookahead proves another page exists
	if rawCursor != "" {
		cur, err := decodeDeliveryCursor(rawCursor)
		if err != nil {
			return DeliveryPage{}, err
		}
		q.Cursor = &cur
	}
	rows, err := s.store.ListDeliveries(ctx, ws, endpointID, q)
	if err != nil {
		return DeliveryPage{}, err
	}

	page := DeliveryPage{Items: rows}
	if int32(len(rows)) > limit {
		page.Items = rows[:limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = encodeDeliveryCursor(DeliveryCursor{CreatedAt: last.CreatedAt.Time, ID: last.ID})
	}
	return page, nil
}

// Dispatch fans one Event out to every ACTIVE endpoint in the workspace whose
// event_types is empty (all events) or contains the type. Each match gets a
// webhook_deliveries row plus an enqueued webhook:deliver task.
//
// It is called AFTER the emitting domain's own work has committed, so a fan-out
// failure must never propagate: a per-endpoint error is logged and the loop
// continues to the rest. The method returns nil unless the workspace id is
// unparseable (a programmer error at the call site, worth surfacing).
func (s *Service) Dispatch(ctx context.Context, workspaceID string, e Event) error {
	ws, err := uuid.Parse(workspaceID)
	if err != nil {
		return fmt.Errorf("webhook: dispatch: %w", err)
	}
	endpoints, err := s.store.ListEndpoints(ctx, ws)
	if err != nil {
		slog.WarnContext(ctx, "webhook dispatch could not list endpoints",
			"workspace_id", workspaceID, "event", e.Type, "err", err)
		return nil
	}
	occurredAt := e.OccurredAt
	if occurredAt.IsZero() {
		occurredAt = s.now()
	}
	for _, ep := range endpoints {
		if !ep.Active || !subscribes(ep.EventTypes, e.Type) {
			continue
		}
		if _, err := s.enqueueDelivery(ctx, ep, e.Type, e.Data, occurredAt); err != nil {
			// Logged, not returned: the originating operation already committed,
			// and one unreachable endpoint must not stop the others. Ids only —
			// the payload is the event body and never reaches a log sink.
			slog.WarnContext(ctx, "webhook dispatch failed for one endpoint",
				"workspace_id", workspaceID, "endpoint_id", ep.ID.String(), "event", e.Type, "err", err)
		}
	}
	return nil
}

// enqueueDelivery builds the JSON body {id,event,occurred_at,data}, inserts the
// webhook_deliveries row, and enqueues the deliver task. The delivery id is
// generated up front so it can be embedded in the body before the row is
// written — the stored payload is byte-identical to what is POSTed.
func (s *Service) enqueueDelivery(ctx context.Context, ep gen.WebhookEndpoint, eventType string, data any, occurredAt time.Time) (gen.WebhookDelivery, error) {
	id := uuid.New()
	if data == nil {
		data = struct{}{}
	}
	body, err := json.Marshal(deliveryBody{
		ID:         id.String(),
		Event:      eventType,
		OccurredAt: occurredAt.UTC().Format(time.RFC3339),
		Data:       data,
	})
	if err != nil {
		return gen.WebhookDelivery{}, fmt.Errorf("webhook: marshal delivery body: %w", err)
	}
	del, err := s.store.CreateDelivery(ctx, CreateDeliveryParams{
		ID: id, EndpointID: ep.ID, WorkspaceID: ep.WorkspaceID, EventType: eventType, Payload: body,
	})
	if err != nil {
		return gen.WebhookDelivery{}, fmt.Errorf("webhook: create delivery: %w", err)
	}
	if err := s.enq.EnqueueWebhookDeliver(ctx, id.String(), ep.WorkspaceID.String()); err != nil {
		// The row exists and is 'pending' with next_attempt_at=now(); a reconcile
		// sweep (or a manual replay) can still pick it up. Surface the error so a
		// synchronous caller (Ping) reports it.
		return del, fmt.Errorf("webhook: enqueue delivery: %w", err)
	}
	return del, nil
}

// deliveryBody is the exact wire shape of a webhook POST body.
type deliveryBody struct {
	ID         string `json:"id"`
	Event      string `json:"event"`
	OccurredAt string `json:"occurred_at"`
	Data       any    `json:"data"`
}

// mintSecret generates a fresh 32-byte secret, returns it base64-encoded (the
// form the operator copies and the HMAC key both sides use), and seals it under
// the workspace DEK for storage.
func (s *Service) mintSecret(ctx context.Context, ws uuid.UUID) (raw string, sealed []byte, err error) {
	buf := make([]byte, secretBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", nil, fmt.Errorf("webhook: mint secret: %w", err)
	}
	raw = base64.StdEncoding.EncodeToString(buf)
	sealer, err := s.keyring.SealerFor(ctx, ws)
	if err != nil {
		return "", nil, fmt.Errorf("webhook: keyring: %w", err)
	}
	token, err := sealer.Seal([]byte(raw))
	if err != nil {
		return "", nil, fmt.Errorf("webhook: seal secret: %w", err)
	}
	return raw, []byte(token), nil
}

// vet parses and SSRF-checks a receiver URL and returns its normalised string
// form (what gets stored). Length is capped first so a pathological URL is
// rejected before any DNS work.
func (s *Service) vet(ctx context.Context, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("%w: url is required", ErrValidation)
	}
	if len(raw) > maxURLLen {
		return "", fmt.Errorf("%w: url is too long", ErrValidation)
	}
	u, err := webhookwire.VetURL(ctx, raw, s.allowPrivate)
	if err != nil {
		// The wire guard's ErrBlockedURL is a caller-fixable input problem — fold
		// it onto ErrValidation so the handler maps it to 422 with the message.
		return "", fmt.Errorf("%w: %s", ErrValidation, strings.TrimPrefix(err.Error(), "webhook: "))
	}
	return u.String(), nil
}

func validateDescription(d string) (string, error) {
	d = strings.TrimSpace(d)
	if len(d) > maxDescriptionLen {
		return "", fmt.Errorf("%w: description must be at most %d characters", ErrValidation, maxDescriptionLen)
	}
	return d, nil
}

// validateEventTypes rejects any entry outside the subscribable catalog and
// deduplicates. A nil input means "no filter given"; the caller decides whether
// that is "all events" (create) or "unchanged" (update).
func validateEventTypes(in []string) ([]string, error) {
	if in == nil {
		return []string{}, nil
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, t := range in {
		t = strings.TrimSpace(t)
		if !slices.Contains(subscribable, t) {
			return nil, fmt.Errorf("%w: unknown event type %q (known: %s)", ErrValidation, t, strings.Join(subscribable, ", "))
		}
		if seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out, nil
}

// subscribes reports whether an endpoint with the given event_types filter wants
// eventType. An empty filter is "every event".
func subscribes(filter []string, eventType string) bool {
	if len(filter) == 0 {
		return true
	}
	return slices.Contains(filter, eventType)
}

// translateRead maps "no such row" onto ErrNotFound and leaves everything else
// alone, so a handler distinguishes 404 from 500.
func translateRead(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
