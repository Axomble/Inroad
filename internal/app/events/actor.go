package events

import "context"

// actorKey is this package's private context key. Unexported so nothing outside
// can stash an actor id under it — the value is only ever what WithActor put
// there.
type actorKey struct{}

// WithActor tags ctx with the user who initiated the request.
//
// This travels in the context rather than as a parameter on every service
// method, and that is a deliberate exception to how this codebase passes actor
// attribution (crm's explicit `Actor` argument on ...WithActor methods).
//
// The difference is what the value is FOR. crm's Actor is written to the
// activity feed — it is part of the record, so it must be explicit, reviewable,
// and impossible to forget. This one exists solely so a client can recognise and
// drop the echo of its own action, which is a transport concern that no domain
// method's signature should have to grow a parameter for. Threading it through
// Launch, MoveDeal, every mailbox mutation and their twelve test callers would
// put a socket detail in the signature of code that has nothing to do with
// sockets.
//
// It is safe to omit: a missing actor yields "", which means "system" on the
// wire, and the client's guard treats an actorless event as "not mine". The
// failure mode of forgetting it is a redundant refetch, never a wrong one.
func WithActor(ctx context.Context, userID string) context.Context {
	if userID == "" {
		return ctx
	}
	return context.WithValue(ctx, actorKey{}, userID)
}

// ActorFrom reads the initiating user id, or "" when the context carries none
// (a worker, a scheduled task, a test).
func ActorFrom(ctx context.Context) string {
	id, _ := ctx.Value(actorKey{}).(string)
	return id
}
