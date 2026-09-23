package audit

import (
	"context"
	"net/http"

	"github.com/inroad/inroad/internal/platform/httpx"
)

type actorKey struct{}

type requestKey struct{}

type requestMeta struct{ ip, userAgent string }

// WithActor attributes every event built from ctx (New) to actor.
//
// Set centrally rather than by each call site: auth.RequireAuth stores the
// authenticated principal's actor, and the agent tool registry overrides it
// with an agent actor for the duration of a tool call. A domain service
// therefore cannot forget attribution, and cannot mis-attribute an agent's
// action to the human who delegated it.
func WithActor(ctx context.Context, a Actor) context.Context {
	return context.WithValue(ctx, actorKey{}, a)
}

// ActorFrom returns the actor WithActor stored, if any.
func ActorFrom(ctx context.Context) (Actor, bool) {
	a, ok := ctx.Value(actorKey{}).(Actor)
	return a, ok
}

// WithRequest stores the client address and user agent for events built from
// ctx. ip is a bare address ("" when indeterminate).
func WithRequest(ctx context.Context, ip, userAgent string) context.Context {
	return context.WithValue(ctx, requestKey{}, requestMeta{ip: ip, userAgent: userAgent})
}

// RequestFrom returns what WithRequest stored ("" and "" when nothing did — a
// worker, the CLI, a test).
func RequestFrom(ctx context.Context) (ip, userAgent string) {
	m, _ := ctx.Value(requestKey{}).(requestMeta)
	return m.ip, m.userAgent
}

// RequestMiddleware stores each request's client IP and user agent on its
// context. The IP comes from the shared resolver, which honours
// X-Forwarded-For only from INROAD_TRUSTED_PROXIES — the same address the
// session rows and the api-key allowlist see. Mounted at the router root so
// public routes (sign-in) carry it too.
func RequestMiddleware(ips httpx.ClientIPResolver) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := ""
			if addr := ips.ClientIP(r); addr.IsValid() {
				ip = addr.String()
			}
			next.ServeHTTP(w, r.WithContext(WithRequest(r.Context(), ip, r.UserAgent())))
		})
	}
}
