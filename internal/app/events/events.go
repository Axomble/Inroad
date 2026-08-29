// Package events is the control plane's seam onto realtime fan-out.
//
// It exists so a domain service can announce something happened without
// depending on internal/platform/realtime — the same reasoning
// campaign.Enqueuer uses to avoid depending on platform/queue. Domains import
// this tiny package; the composition root wires the real hub behind it.
//
// The worker reaches realtime through coreapi.RealtimeClient instead, because it
// is a separate process and everything it does crosses that boundary. This is
// the API-process counterpart, and the two deliberately do not share a type: one
// is a transport-neutral string contract for a future HTTP seam, the other is an
// in-process call.
package events

import (
	"context"
	"time"
)

// Publisher announces one workspace event to connected clients.
//
// A nil Publisher is the "realtime disabled" configuration and every helper here
// treats it as a no-op, so a service constructed without one behaves exactly as
// it did before sockets existed. That is the property that lets a domain emit
// unconditionally without every call site re-deciding whether realtime is on.
type Publisher interface {
	// Publish fans ev out to workspaceID's clients.
	//
	// Implementations must be safe to ignore: callers announce AFTER their real
	// work has committed, so a publish failure can only cost a browser its
	// notification, never the operation itself.
	Publish(ctx context.Context, workspaceID string, ev Event) error
}

// Event is one thing that happened, in the shape the wire carries.
//
// Data is deliberately a map rather than a typed payload per event: the envelope
// is opaque by design (spec §5), so adding an event type needs a new client
// handler and nothing else — no change here, no transport migration.
type Event struct {
	// Type is the dotted event name, e.g. "campaign.launched".
	Type string
	// SubjectKind and SubjectID say what the event is about, so a client can
	// decide whether it cares without decoding Data.
	SubjectKind string
	SubjectID   string
	// ActorID is the user who caused this, empty for system-originated events.
	//
	// It is what stops an optimistic update fighting its own echo: the client
	// drops events it originated. Omitting it on a user-initiated action is a
	// real bug — the actor's own UI will visibly snap back — which is why the
	// helpers below take it explicitly rather than defaulting it.
	ActorID string
	// OccurredAt is when the thing happened, not when it was published.
	OccurredAt time.Time
	// Data carries IDS AND MINIMAL DISPLAY FIELDS ONLY.
	//
	// This is a security boundary, not a size optimisation (spec §7.2-7.3). A
	// socket event must not become a way to read what the REST endpoints would
	// have gated — the client refetches through those, authorized — and it
	// carries no recipient PII.
	Data map[string]any
}

// Emit publishes ev and reports whether anything was sent.
//
// It swallows the publish error on purpose. Every caller emits AFTER committing
// real work, so the only thing a failure can cost is a notification a client
// will pick up on its next refetch. Propagating it would invite a caller to
// abort — or worse, retry — an operation that already succeeded.
//
// The bool is for tests and for a caller that wants to log; production callers
// ignore it.
func Emit(ctx context.Context, p Publisher, workspaceID string, ev Event) bool {
	if p == nil {
		return false
	}
	return p.Publish(ctx, workspaceID, ev) == nil
}
