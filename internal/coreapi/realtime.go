package coreapi

import (
	"context"
	"time"
)

// RealtimeClient is an optional execution-plane capability: publishing one
// realtime event to a workspace's connected browsers. Kept separate from Client
// — like DeadLetterClient and CRMCaptureClient — so every worker fake and a
// future remote client are not forced to implement it.
//
// A Client that does not implement it simply publishes nothing, which degrades
// to today's behaviour (the browser learns about the change on its next poll or
// refetch) rather than to a failed task. That is the right default: a socket
// event is an OPTIMISATION over polling, never the only path by which the UI
// finds out. A worker must never fail real work because a browser could not be
// notified.
//
// Why this exists at all: the worker is a separate process from the API
// (cmd/worker vs cmd/inroad), so an in-process Go channel reaches no browser.
// Every worker-originated event has to cross Redis, and this is the seam it
// crosses at.
type RealtimeClient interface {
	// PublishRealtime fans one event out to WorkspaceID's connected clients.
	//
	// Implementations must treat a publish failure as non-fatal for the caller's
	// real work: return the error so it can be LOGGED, never so it can abort a
	// send, a classification or a database write that already succeeded.
	PublishRealtime(ctx context.Context, in RealtimeEventInput) error
}

// RealtimeEventInput is one event as the execution plane observed it.
//
// Ids are strings for the same reason every other coreapi id is: the seam is
// transport-neutral and a future HTTP implementation carries them as text.
type RealtimeEventInput struct {
	WorkspaceID string
	// Type is the dotted event name, e.g. "inbox.message.created". A new type
	// needs a new client handler and nothing else — no transport change.
	Type string
	// SubjectKind and SubjectID say what the event is about ("thread", id), so a
	// client can decide whether it cares without decoding Data.
	SubjectKind string
	SubjectID   string
	// ActorID is the user who caused the event, empty for system-originated ones
	// (a poller, a scheduled send). The client drops events it originated, or an
	// optimistic update fights the echo of the actor's own action.
	//
	// Worker-originated events legitimately have no actor: nobody clicked to make
	// an inbound reply arrive.
	ActorID string
	// OccurredAt is when the thing happened, not when it was published.
	OccurredAt time.Time
	// Data is type-specific and MINIMAL — ids and small display fields only.
	//
	// This is a security boundary, not a size optimisation. A socket event must
	// not become a way to read data the REST endpoints would have gated: the
	// client refetches through those, authorized, for anything sensitive. It
	// carries no recipient PII, for the same reason the bounce path declines to
	// log Final-Recipient.
	Data map[string]any
}
