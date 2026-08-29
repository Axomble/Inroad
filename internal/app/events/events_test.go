package events

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/realtime"
)

type recorder struct {
	workspaces []string
	events     []Event
	err        error
}

func (r *recorder) Publish(_ context.Context, workspaceID string, ev Event) error {
	if r.err != nil {
		return r.err
	}
	r.workspaces = append(r.workspaces, workspaceID)
	r.events = append(r.events, ev)
	return nil
}

func TestEmit_PublishesToTheWorkspace(t *testing.T) {
	rec := &recorder{}
	ev := Event{Type: "campaign.launched", SubjectKind: "campaign", SubjectID: "c1"}

	if !Emit(context.Background(), rec, "ws1", ev) {
		t.Fatal("Emit reported no publish")
	}
	if len(rec.events) != 1 || rec.workspaces[0] != "ws1" {
		t.Fatalf("published %d events to %v", len(rec.events), rec.workspaces)
	}
	if rec.events[0].Type != "campaign.launched" {
		t.Errorf("Type = %q", rec.events[0].Type)
	}
}

// A nil Publisher is the "realtime disabled" configuration, not a bug. Every
// domain emits unconditionally and relies on this, so it must never panic.
func TestEmit_NilPublisherIsASilentNoOp(t *testing.T) {
	if Emit(context.Background(), nil, "ws1", Event{Type: "campaign.launched"}) {
		t.Error("Emit reported a publish through a nil Publisher")
	}
}

// A publish failure must not reach the caller: every emit happens AFTER the real
// work committed, so the only thing at stake is a notification.
func TestEmit_SwallowsAPublishFailure(t *testing.T) {
	rec := &recorder{err: errors.New("redis is down")}

	if Emit(context.Background(), rec, "ws1", Event{Type: "deal.moved"}) {
		t.Error("Emit reported success despite a publish error")
	}
	// The point is that it returned at all rather than propagating.
}

func TestActor_RoundTrips(t *testing.T) {
	ctx := WithActor(context.Background(), "user-1")

	if got := ActorFrom(ctx); got != "user-1" {
		t.Errorf("ActorFrom = %q, want user-1", got)
	}
}

// A context with no actor is the normal worker/scheduled-task case, and an empty
// string must not be stored as a value — a client reading actor_id then sees the
// field absent rather than blank.
func TestActor_AbsentAndEmptyBothYieldNoActor(t *testing.T) {
	if got := ActorFrom(context.Background()); got != "" {
		t.Errorf("ActorFrom(bare ctx) = %q, want empty", got)
	}
	if got := ActorFrom(WithActor(context.Background(), "")); got != "" {
		t.Errorf("ActorFrom(empty actor) = %q, want empty", got)
	}
}

// --- the hub adapter -------------------------------------------------------

type fakeHub struct {
	calls []struct {
		workspaceID uuid.UUID
		envelope    realtime.Envelope
	}
	err error
}

func (h *fakeHub) Publish(_ context.Context, workspaceID uuid.UUID, ev realtime.Envelope) (int64, error) {
	if h.err != nil {
		return 0, h.err
	}
	h.calls = append(h.calls, struct {
		workspaceID uuid.UUID
		envelope    realtime.Envelope
	}{workspaceID, ev})
	return int64(len(h.calls)), nil
}

func TestHubPublisher_TranslatesTheEventOntoTheEnvelope(t *testing.T) {
	hub := &fakeHub{}
	pub := NewHubPublisher(hub)
	ws := uuid.New()
	at := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)

	err := pub.Publish(context.Background(), ws.String(), Event{
		Type: "deal.moved", SubjectKind: "deal", SubjectID: "d1",
		ActorID: "u1", OccurredAt: at, Data: map[string]any{"deal_id": "d1"},
	})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	got := hub.calls[0]
	if got.workspaceID != ws {
		t.Errorf("workspace = %v, want %v — an event on the wrong channel is a cross-tenant leak", got.workspaceID, ws)
	}
	if got.envelope.Type != "deal.moved" || got.envelope.ActorID != "u1" {
		t.Errorf("envelope = %+v", got.envelope)
	}
	if !got.envelope.At.Equal(at) {
		t.Errorf("At = %v, want %v", got.envelope.At, at)
	}
	var data map[string]any
	if err := json.Unmarshal(got.envelope.Data, &data); err != nil {
		t.Fatalf("data is not valid JSON: %v", err)
	}
	if data["deal_id"] != "d1" {
		t.Errorf("data = %#v", data)
	}
}

// NewHubPublisher(nil) must return an untyped nil, NOT a wrapper around a nil
// hub. The latter is the classic Go trap: the interface is non-nil, so Emit's
// `p == nil` check misses and every publish dereferences a nil pointer.
func TestNewHubPublisher_NilHubYieldsANilPublisher(t *testing.T) {
	pub := NewHubPublisher(nil)

	if pub != nil {
		t.Fatal("NewHubPublisher(nil) returned a non-nil Publisher; Emit's nil check would be bypassed")
	}
	// And it is safe to hand straight to Emit, which is what a composition root
	// without a hub actually does.
	if Emit(context.Background(), pub, uuid.NewString(), Event{Type: "x"}) {
		t.Error("Emit reported a publish through a nil hub publisher")
	}
}

func TestHubPublisher_RejectsAnUnparseableWorkspace(t *testing.T) {
	hub := &fakeHub{}

	if err := NewHubPublisher(hub).Publish(context.Background(), "not-a-uuid", Event{Type: "x"}); err == nil {
		t.Error("err = nil for an unparseable workspace")
	}
	if len(hub.calls) != 0 {
		t.Errorf("published %d events despite a bad workspace", len(hub.calls))
	}
}

// Nil Data must stay absent on the wire rather than becoming the JSON literal
// "null", which a client reading `data` would have to special-case.
func TestHubPublisher_NilDataStaysAbsent(t *testing.T) {
	hub := &fakeHub{}

	if err := NewHubPublisher(hub).Publish(context.Background(), uuid.NewString(),
		Event{Type: "campaign.launched"}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if got := hub.calls[0].envelope.Data; got != nil {
		t.Errorf("Data = %q, want nil", got)
	}
}

func TestHubPublisher_PropagatesAHubFailure(t *testing.T) {
	wantErr := errors.New("redis is down")

	err := NewHubPublisher(&fakeHub{err: wantErr}).Publish(context.Background(), uuid.NewString(), Event{Type: "x"})
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want %v wrapped", err, wantErr)
	}
}
