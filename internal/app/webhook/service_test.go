package webhook

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/platform/crypto"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// --- fakes ---

type fakeStore struct {
	endpoints  []gen.WebhookEndpoint
	deliveries []gen.WebhookDelivery
	createErr  error
}

func (f *fakeStore) ListEndpoints(context.Context, uuid.UUID) ([]gen.WebhookEndpoint, error) {
	out := make([]gen.WebhookEndpoint, len(f.endpoints))
	copy(out, f.endpoints)
	return out, nil
}

func (f *fakeStore) GetEndpoint(_ context.Context, ws, id uuid.UUID) (gen.WebhookEndpoint, error) {
	for _, e := range f.endpoints {
		if e.ID == id && e.WorkspaceID == ws {
			return e, nil
		}
	}
	return gen.WebhookEndpoint{}, pgx.ErrNoRows
}

func (f *fakeStore) CreateEndpoint(_ context.Context, ws uuid.UUID, in CreateParams) (gen.WebhookEndpoint, error) {
	e := gen.WebhookEndpoint{
		ID: uuid.New(), WorkspaceID: ws, Url: in.URL, Description: in.Description,
		SecretCiphertext: in.SealedSecret, EventTypes: in.EventTypes, Active: true,
		CreatedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
		UpdatedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}
	f.endpoints = append(f.endpoints, e)
	return e, nil
}

func (f *fakeStore) UpdateEndpoint(_ context.Context, ws, id uuid.UUID, in UpdateParams) (gen.WebhookEndpoint, error) {
	for i, e := range f.endpoints {
		if e.ID != id || e.WorkspaceID != ws {
			continue
		}
		if in.URL != nil {
			f.endpoints[i].Url = *in.URL
		}
		if in.Description != nil {
			f.endpoints[i].Description = *in.Description
		}
		if in.EventTypes != nil {
			f.endpoints[i].EventTypes = in.EventTypes
		}
		if in.Active != nil {
			f.endpoints[i].Active = *in.Active
		}
		return f.endpoints[i], nil
	}
	return gen.WebhookEndpoint{}, pgx.ErrNoRows
}

func (f *fakeStore) RotateEndpointSecret(_ context.Context, ws, id uuid.UUID, sealed []byte) (gen.WebhookEndpoint, error) {
	for i, e := range f.endpoints {
		if e.ID == id && e.WorkspaceID == ws {
			f.endpoints[i].SecretCiphertext = sealed
			return f.endpoints[i], nil
		}
	}
	return gen.WebhookEndpoint{}, pgx.ErrNoRows
}

func (f *fakeStore) DeleteEndpoint(_ context.Context, ws, id uuid.UUID) (bool, error) {
	for i, e := range f.endpoints {
		if e.ID == id && e.WorkspaceID == ws {
			f.endpoints = append(f.endpoints[:i], f.endpoints[i+1:]...)
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeStore) CreateDelivery(_ context.Context, in CreateDeliveryParams) (gen.WebhookDelivery, error) {
	if f.createErr != nil {
		return gen.WebhookDelivery{}, f.createErr
	}
	d := gen.WebhookDelivery{
		ID: in.ID, EndpointID: in.EndpointID, WorkspaceID: in.WorkspaceID,
		EventType: in.EventType, Payload: in.Payload, Status: "pending",
		CreatedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}
	f.deliveries = append(f.deliveries, d)
	return d, nil
}

func (f *fakeStore) ListDeliveries(_ context.Context, ws, endpointID uuid.UUID, q DeliveryQuery) ([]gen.WebhookDelivery, error) {
	var out []gen.WebhookDelivery
	for _, d := range f.deliveries {
		if d.WorkspaceID == ws && d.EndpointID == endpointID {
			out = append(out, d)
		}
	}
	return out, nil
}

var _ Store = (*fakeStore)(nil)

type fakeEnqueuer struct {
	calls   int
	failErr error
}

func (f *fakeEnqueuer) EnqueueWebhookDeliver(context.Context, string, string) error {
	f.calls++
	return f.failErr
}

func testKeyring(t *testing.T) *crypto.Keyring {
	t.Helper()
	provider, err := crypto.NewLocalKeyProvider(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatalf("NewLocalKeyProvider: %v", err)
	}
	return crypto.NewKeyring(provider, &memDEKStore{rows: map[uuid.UUID][]byte{}}, nil)
}

type memDEKStore struct{ rows map[uuid.UUID][]byte }

func (m *memDEKStore) GetWrappedDEK(_ context.Context, ws uuid.UUID) ([]byte, string, error) {
	if b, ok := m.rows[ws]; ok {
		return b, "local", nil
	}
	return nil, "", crypto.ErrDEKNotFound
}

func (m *memDEKStore) PutWrappedDEK(_ context.Context, ws uuid.UUID, wrapped []byte, _ string) error {
	if _, ok := m.rows[ws]; ok {
		return errors.New("exists")
	}
	m.rows[ws] = wrapped
	return nil
}

func newService(t *testing.T, store *fakeStore, enq Enqueuer) *Service {
	t.Helper()
	return NewService(store, testKeyring(t), enq, false)
}

func endpoint(ws uuid.UUID, active bool, types ...string) gen.WebhookEndpoint {
	if types == nil {
		types = []string{}
	}
	return gen.WebhookEndpoint{
		ID: uuid.New(), WorkspaceID: ws, Url: "https://8.8.8.8/hook", Active: active, EventTypes: types,
		CreatedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
		UpdatedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}
}

// --- tests ---

// Dispatch fans out to active + subscribed endpoints only, and an empty
// event_types filter means "every event".
func TestDispatchFanOut(t *testing.T) {
	ws := uuid.New()
	subscribed := endpoint(ws, true, EventReplyReceived)
	allEvents := endpoint(ws, true)                     // empty filter = all
	otherEvent := endpoint(ws, true, EventEmailBounced) // not this type
	inactive := endpoint(ws, false, EventReplyReceived) // subscribed but off
	store := &fakeStore{endpoints: []gen.WebhookEndpoint{subscribed, allEvents, otherEvent, inactive}}
	enq := &fakeEnqueuer{}
	svc := newService(t, store, enq)

	if err := svc.Dispatch(context.Background(), ws.String(), Event{Type: EventReplyReceived, Data: map[string]any{"x": 1}}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	if len(store.deliveries) != 2 {
		t.Fatalf("expected 2 deliveries (subscribed + all-events), got %d", len(store.deliveries))
	}
	if enq.calls != 2 {
		t.Fatalf("expected 2 enqueues, got %d", enq.calls)
	}
	got := map[uuid.UUID]bool{}
	for _, d := range store.deliveries {
		got[d.EndpointID] = true
	}
	if !got[subscribed.ID] || !got[allEvents.ID] {
		t.Fatalf("wrong endpoints got a delivery: %+v", got)
	}
}

// A dispatch failure (the delivery insert errors for every endpoint) is
// swallowed — Dispatch returns nil so the originating operation is unaffected.
func TestDispatchErrorDoesNotPropagate(t *testing.T) {
	ws := uuid.New()
	store := &fakeStore{
		endpoints: []gen.WebhookEndpoint{endpoint(ws, true)},
		createErr: errors.New("db down"),
	}
	svc := newService(t, store, &fakeEnqueuer{})
	if err := svc.Dispatch(context.Background(), ws.String(), Event{Type: EventEmailBounced}); err != nil {
		t.Fatalf("Dispatch should swallow the per-endpoint error, got %v", err)
	}
}

// An enqueue failure during Dispatch is likewise swallowed (the row exists and a
// sweep can recover it).
func TestDispatchEnqueueFailureDoesNotPropagate(t *testing.T) {
	ws := uuid.New()
	store := &fakeStore{endpoints: []gen.WebhookEndpoint{endpoint(ws, true)}}
	svc := newService(t, store, &fakeEnqueuer{failErr: errors.New("redis down")})
	if err := svc.Dispatch(context.Background(), ws.String(), Event{Type: EventEmailBounced}); err != nil {
		t.Fatalf("Dispatch should swallow the enqueue error, got %v", err)
	}
	if len(store.deliveries) != 1 {
		t.Fatalf("the delivery row should still have been written, got %d", len(store.deliveries))
	}
}

func TestCreateRejectsSSRFURL(t *testing.T) {
	ws := uuid.New()
	svc := newService(t, &fakeStore{}, &fakeEnqueuer{})
	for _, u := range []string{"http://127.0.0.1/hook", "https://10.0.0.5/hook", "ftp://8.8.8.8/x"} {
		if _, _, err := svc.Create(context.Background(), ws, CreateInput{URL: u}); !errors.Is(err, ErrValidation) {
			t.Errorf("Create(%q) = %v, want ErrValidation", u, err)
		}
	}
}

func TestUpdateRejectsSSRFURL(t *testing.T) {
	ws := uuid.New()
	store := &fakeStore{}
	svc := newService(t, store, &fakeEnqueuer{})
	ep, _, err := svc.Create(context.Background(), ws, CreateInput{URL: "https://8.8.8.8/hook"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	bad := "http://169.254.169.254/latest"
	if _, err := svc.Update(context.Background(), ws, ep.ID, UpdateInput{URL: &bad}); !errors.Is(err, ErrValidation) {
		t.Fatalf("Update with SSRF url = %v, want ErrValidation", err)
	}
}

func TestCreateRejectsUnknownEventType(t *testing.T) {
	ws := uuid.New()
	svc := newService(t, &fakeStore{}, &fakeEnqueuer{})
	_, _, err := svc.Create(context.Background(), ws, CreateInput{
		URL: "https://8.8.8.8/hook", EventTypes: []string{"reply.received", "totally.made.up"},
	})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("Create with unknown event type = %v, want ErrValidation", err)
	}
}

// RotateSecret swaps the stored ciphertext and hands back a different raw secret.
func TestRotateSecretChangesStoredCiphertext(t *testing.T) {
	ws := uuid.New()
	store := &fakeStore{}
	svc := newService(t, store, &fakeEnqueuer{})
	ep, secret1, err := svc.Create(context.Background(), ws, CreateInput{URL: "https://8.8.8.8/hook"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	before := append([]byte(nil), store.endpoints[0].SecretCiphertext...)

	ep2, secret2, err := svc.RotateSecret(context.Background(), ws, ep.ID)
	if err != nil {
		t.Fatalf("RotateSecret: %v", err)
	}
	if ep2.ID != ep.ID {
		t.Fatal("RotateSecret returned a different endpoint")
	}
	if secret1 == secret2 {
		t.Fatal("RotateSecret returned the same raw secret")
	}
	if bytes.Equal(before, store.endpoints[0].SecretCiphertext) {
		t.Fatal("RotateSecret did not change the stored ciphertext")
	}
}

func TestRotateSecretUnknownEndpointIsNotFound(t *testing.T) {
	svc := newService(t, &fakeStore{}, &fakeEnqueuer{})
	if _, _, err := svc.RotateSecret(context.Background(), uuid.New(), uuid.New()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("RotateSecret(unknown) = %v, want ErrNotFound", err)
	}
}

// Ping creates a delivery regardless of the endpoint's subscription filter.
func TestPingIgnoresSubscriptionFilter(t *testing.T) {
	ws := uuid.New()
	store := &fakeStore{endpoints: []gen.WebhookEndpoint{endpoint(ws, true, EventReplyReceived)}}
	enq := &fakeEnqueuer{}
	svc := newService(t, store, enq)
	del, err := svc.Ping(context.Background(), ws, store.endpoints[0].ID)
	if err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if del.EventType != EventPing {
		t.Fatalf("ping delivery event_type = %q, want %q", del.EventType, EventPing)
	}
	if enq.calls != 1 || len(store.deliveries) != 1 {
		t.Fatalf("Ping did not enqueue exactly one delivery: calls=%d rows=%d", enq.calls, len(store.deliveries))
	}
}
