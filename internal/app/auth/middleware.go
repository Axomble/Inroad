package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/inroad/inroad/internal/platform/httpx"
)

type ctxKey struct{}

// RequireAuth rejects requests without a valid Bearer token and stores the
// resulting Claims in the request context.
func RequireAuth(secret []byte) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := r.Header.Get("Authorization")
			token, ok := strings.CutPrefix(h, "Bearer ")
			if !ok {
				httpx.Error(w, http.StatusUnauthorized, "missing bearer token")
				return
			}
			claims, err := ParseToken(secret, token)
			if err != nil {
				httpx.Error(w, http.StatusUnauthorized, "invalid token")
				return
			}
			ctx := context.WithValue(r.Context(), ctxKey{}, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// UserFromContext returns the authenticated claims, if present.
func UserFromContext(ctx context.Context) (Claims, bool) {
	c, ok := ctx.Value(ctxKey{}).(Claims)
	return c, ok
}

var roleRank = map[string]int{"member": 1, "admin": 2, "owner": 3}

// RequireRole rejects (403) callers whose workspace role ranks below min.
// Must run after RequireAuth.
func RequireRole(min string) func(http.Handler) http.Handler {
	want, ok := roleRank[min]
	if !ok {
		panic("auth.RequireRole: unknown role " + min)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, ok := UserFromContext(r.Context())
			if !ok || roleRank[c.Role] < want {
				httpx.Error(w, http.StatusForbidden, "insufficient role")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
