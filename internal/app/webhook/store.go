package webhook

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// Store is the repository interface this domain depends on (defined by the
// consumer, backed by sqlc). gen.WebhookEndpoint / gen.WebhookDelivery are the
// persistence types — no parallel entity struct, per the repo's "sqlc models are
// the persistence type; the interface boundary is where the decoupling lives"
// rule.
//
// Every method takes the workspace explicitly and pins it in the SQL WHERE
// (docs/security.md invariant 4): a signing secret and a receiver's event stream
// are exactly what must never cross a tenant boundary.
type Store interface {
	ListEndpoints(ctx context.Context, ws uuid.UUID) ([]gen.WebhookEndpoint, error)
	GetEndpoint(ctx context.Context, ws, id uuid.UUID) (gen.WebhookEndpoint, error)
	CreateEndpoint(ctx context.Context, ws uuid.UUID, in CreateParams) (gen.WebhookEndpoint, error)
	UpdateEndpoint(ctx context.Context, ws, id uuid.UUID, in UpdateParams) (gen.WebhookEndpoint, error)
	RotateEndpointSecret(ctx context.Context, ws, id uuid.UUID, sealed []byte) (gen.WebhookEndpoint, error)
	DeleteEndpoint(ctx context.Context, ws, id uuid.UUID) (bool, error)

	CreateDelivery(ctx context.Context, in CreateDeliveryParams) (gen.WebhookDelivery, error)
	ListDeliveries(ctx context.Context, ws, endpointID uuid.UUID, q DeliveryQuery) ([]gen.WebhookDelivery, error)
}

// CreateParams is the sealed, validated input for a new endpoint. The secret is
// already sealed under the workspace DEK by the service — the store never sees a
// plaintext secret.
type CreateParams struct {
	URL          string
	Description  string
	EventTypes   []string
	SealedSecret []byte
}

// UpdateParams is a PATCH: a nil pointer (or nil EventTypes) means "leave this
// column alone". An empty non-nil EventTypes is a real change — "subscribe to
// every event".
type UpdateParams struct {
	URL         *string
	Description *string
	EventTypes  []string
	Active      *bool
}

// CreateDeliveryParams carries a caller-generated id so the JSON body (which
// embeds that id) is built before the row is written.
type CreateDeliveryParams struct {
	ID          uuid.UUID
	EndpointID  uuid.UUID
	WorkspaceID uuid.UUID
	EventType   string
	Payload     []byte
}

// DeliveryQuery is one keyset page of an endpoint's delivery log. Cursor is nil
// for the first page.
type DeliveryQuery struct {
	Cursor *DeliveryCursor
	Limit  int32
}

// PgStore implements Store over the sqlc-generated queries.
type PgStore struct{ q *gen.Queries }

// NewPgStore builds a PgStore over a pgx pool.
func NewPgStore(q *gen.Queries) *PgStore { return &PgStore{q: q} }

var _ Store = (*PgStore)(nil)

func (s *PgStore) ListEndpoints(ctx context.Context, ws uuid.UUID) ([]gen.WebhookEndpoint, error) {
	return s.q.ListWebhookEndpoints(ctx, ws)
}

func (s *PgStore) GetEndpoint(ctx context.Context, ws, id uuid.UUID) (gen.WebhookEndpoint, error) {
	return s.q.GetWebhookEndpoint(ctx, gen.GetWebhookEndpointParams{WorkspaceID: ws, ID: id})
}

func (s *PgStore) CreateEndpoint(ctx context.Context, ws uuid.UUID, in CreateParams) (gen.WebhookEndpoint, error) {
	return s.q.CreateWebhookEndpoint(ctx, gen.CreateWebhookEndpointParams{
		WorkspaceID:      ws,
		Url:              in.URL,
		Description:      in.Description,
		SecretCiphertext: in.SealedSecret,
		EventTypes:       normalizeArray(in.EventTypes),
		Active:           true,
	})
}

func (s *PgStore) UpdateEndpoint(ctx context.Context, ws, id uuid.UUID, in UpdateParams) (gen.WebhookEndpoint, error) {
	return s.q.UpdateWebhookEndpoint(ctx, gen.UpdateWebhookEndpointParams{
		WorkspaceID: ws,
		ID:          id,
		Url:         in.URL,
		Description: in.Description,
		EventTypes:  in.EventTypes,
		Active:      in.Active,
	})
}

func (s *PgStore) RotateEndpointSecret(ctx context.Context, ws, id uuid.UUID, sealed []byte) (gen.WebhookEndpoint, error) {
	return s.q.RotateWebhookEndpointSecret(ctx, gen.RotateWebhookEndpointSecretParams{
		WorkspaceID: ws, ID: id, SecretCiphertext: sealed,
	})
}

func (s *PgStore) DeleteEndpoint(ctx context.Context, ws, id uuid.UUID) (bool, error) {
	n, err := s.q.DeleteWebhookEndpoint(ctx, gen.DeleteWebhookEndpointParams{WorkspaceID: ws, ID: id})
	return n > 0, err
}

func (s *PgStore) CreateDelivery(ctx context.Context, in CreateDeliveryParams) (gen.WebhookDelivery, error) {
	return s.q.CreateWebhookDelivery(ctx, gen.CreateWebhookDeliveryParams{
		ID:          in.ID,
		EndpointID:  in.EndpointID,
		WorkspaceID: in.WorkspaceID,
		EventType:   in.EventType,
		Payload:     in.Payload,
	})
}

func (s *PgStore) ListDeliveries(ctx context.Context, ws, endpointID uuid.UUID, q DeliveryQuery) ([]gen.WebhookDelivery, error) {
	params := gen.ListWebhookDeliveriesParams{
		WorkspaceID: ws,
		EndpointID:  endpointID,
		PageLimit:   q.Limit,
	}
	if q.Cursor != nil {
		params.Seek = true
		params.CursorTime = pgtype.Timestamptz{Time: q.Cursor.CreatedAt, Valid: true}
		params.CursorID = q.Cursor.ID
	}
	return s.q.ListWebhookDeliveries(ctx, params)
}

// normalizeArray maps a nil slice to a non-nil empty one so the column is
// written as '{}' rather than SQL NULL — the migration's DEFAULT and NOT NULL
// both say the empty array is the "all events" value, never NULL.
func normalizeArray(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}
