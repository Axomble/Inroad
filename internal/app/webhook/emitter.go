package webhook

import (
	"context"
	"time"
)

// Event is one thing that happened in another domain, ready to fan out. Data is
// the event-specific body the EMITTING domain builds (it knows the shape); this
// package only frames it as {id,event,occurred_at,data} per delivery.
type Event struct {
	// Type is one of the catalog constants (reply.received, email.bounced,
	// contact.unsubscribed). "ping" is produced only by Service.Ping.
	Type string
	// Data is marshalled verbatim into the delivery body's "data" field. It
	// carries ids + minimal display fields only — never a credential, never a
	// full message body — the same discipline realtime events follow.
	Data any
	// OccurredAt is when the underlying thing happened. Zero means "now".
	OccurredAt time.Time
}

// Emitter is the seam a domain service announces a webhook-eligible event
// through, AFTER its own work has committed.
//
// A nil Emitter is the "webhooks disabled" configuration: Emit below is then a
// no-op, so a service constructed without one behaves exactly as it did before
// this feature. That is what lets a call site emit unconditionally instead of
// re-deciding whether webhooks are on.
type Emitter interface {
	Emit(ctx context.Context, workspaceID string, e Event) error
}

// Emit calls emitter unless it is nil, and reports whether the dispatch
// succeeded. It never propagates the error: every call site emits after
// committing real work, so a fan-out failure must cost at most a missed webhook,
// never the originating operation. The bool is for a caller that wants to log.
func Emit(ctx context.Context, emitter Emitter, workspaceID string, e Event) bool {
	if emitter == nil {
		return false
	}
	return emitter.Emit(ctx, workspaceID, e) == nil
}

// ServiceEmitter is the real Emitter. It turns an Event into webhook_deliveries
// rows plus enqueued webhook:deliver tasks via Service.Dispatch. The composition
// root builds one and passes it to the emitting domains.
type ServiceEmitter struct{ svc *Service }

// NewServiceEmitter wraps svc as an Emitter.
func NewServiceEmitter(svc *Service) *ServiceEmitter { return &ServiceEmitter{svc: svc} }

// Emit delegates to Dispatch, which does the fan-out and swallows per-endpoint
// failures itself (logging them) so one broken endpoint never blocks the rest.
func (e *ServiceEmitter) Emit(ctx context.Context, workspaceID string, ev Event) error {
	return e.svc.Dispatch(ctx, workspaceID, ev)
}

var _ Emitter = (*ServiceEmitter)(nil)
