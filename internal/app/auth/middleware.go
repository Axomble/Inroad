package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"

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

// WorkspaceID extracts and parses the caller's workspace id from the JWT
// claims stashed on the request context. On failure it writes the HTTP
// error and returns ok=false so the handler can `return` immediately.
// Shared by every route that scopes work to a workspace.
func WorkspaceID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	claims, ok := UserFromContext(r.Context())
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "unauthorized")
		return uuid.Nil, false
	}
	id, err := uuid.Parse(claims.WorkspaceID)
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, "bad workspace")
		return uuid.Nil, false
	}
	return id, true
}

// VerifiedChecker looks up whether a user has confirmed their email address.
// Satisfied by identity.Store; kept as a tiny interface here so auth doesn't
// import the identity package.
type VerifiedChecker interface {
	IsEmailVerified(ctx context.Context, userID uuid.UUID) (bool, error)
}

// RequireVerified rejects callers whose email isn't verified yet (403
// email_not_verified). Must run after RequireAuth: it reads UserID from the
// claims RequireAuth stashed on the context.
func RequireVerified(c VerifiedChecker) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := UserFromContext(r.Context())
			if !ok {
				httpx.Error(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			uid, err := uuid.Parse(claims.UserID)
			if err != nil {
				httpx.Error(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			verified, err := c.IsEmailVerified(r.Context(), uid)
			if err != nil {
				httpx.Error(w, http.StatusInternalServerError, "verify check failed")
				return
			}
			if !verified {
				httpx.Error(w, http.StatusForbidden, "email_not_verified")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
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
