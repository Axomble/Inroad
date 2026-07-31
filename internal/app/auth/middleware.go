package auth

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/httpx"
)

type ctxKey struct{}

// PrincipalKind distinguishes how a request was authenticated. Session is a
// logged-in human (full authority); the machine kinds (added in later phases)
// act with an attenuated, scoped authority.
type PrincipalKind int

const (
	KindSession PrincipalKind = iota
	KindAPIKey
	KindOAuth
)

// Principal is the authenticated caller stashed on the request context by
// RequireAuth. It carries exactly the fields the old Claims type did
// (UserID/WorkspaceID/Role/SessionID) so existing handlers, RequireRole, and
// RequireVerified keep working unchanged — plus Kind and Scopes, which gate
// machine principals (see RequireScope).
type Principal struct {
	UserID      string
	WorkspaceID string
	Role        string
	SessionID   string
	Kind        PrincipalKind
	Scopes      []string
}

// HasScope reports whether the principal may exercise scope. A session
// principal implicitly holds every scope (a human acts with full authority);
// a machine principal holds only the scopes explicitly granted to it.
func (p Principal) HasScope(scope string) bool {
	if p.Kind == KindSession {
		return true
	}
	return slices.Contains(p.Scopes, scope)
}

// ErrUnauthorized is the sentinel a Verifier returns for a DEFINITIVE
// credential rejection (missing/expired/forged token, revoked session,
// token-version mismatch). RequireAuth maps it to 401; any other (non-nil)
// error is treated as an internal failure and mapped to 500, so a transient
// store outage fails loud rather than masquerading as "bad credentials".
var ErrUnauthorized = errors.New("unauthorized")

// Verifier authenticates a request against one credential scheme. It returns:
//   - (principal, true, nil)  — authenticated; RequireAuth stops here.
//   - (_, false, nil)         — "not my credential"; RequireAuth defers to the
//     next verifier (e.g. no Bearer header for a JWT verifier).
//   - (_, false, err)         — hard failure; RequireAuth stops and rejects
//     (401 for ErrUnauthorized, else 500). Use this for a credential that WAS
//     presented but is invalid, so a later verifier can't paper over it.
type Verifier interface {
	Verify(ctx context.Context, r *http.Request) (Principal, bool, error)
}

// RequireAuth builds middleware that authenticates a request against the given
// verifiers in order: the first to return ok wins and its Principal is stashed
// on the context; a hard error stops the chain; if every verifier defers the
// request is rejected 401. At least one verifier must be supplied.
func RequireAuth(verifiers ...Verifier) func(http.Handler) http.Handler {
	if len(verifiers) == 0 {
		panic("auth.RequireAuth: at least one verifier is required")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for _, v := range verifiers {
				p, ok, err := v.Verify(r.Context(), r)
				if err != nil {
					switch {
					case errors.Is(err, ErrRateLimited):
						httpx.Error(w, http.StatusTooManyRequests, "rate limited")
					case errors.Is(err, ErrUnauthorized):
						httpx.Error(w, http.StatusUnauthorized, "invalid token")
					default:
						httpx.Error(w, http.StatusInternalServerError, "auth check failed")
					}
					return
				}
				if ok {
					ctx := context.WithValue(r.Context(), ctxKey{}, p)
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
			}
			httpx.Error(w, http.StatusUnauthorized, "unauthorized")
		})
	}
}

// jwtVerifier is a stateless Verifier: it validates the Bearer access token's
// signature/expiry (alg-pinned) and derives a session Principal directly from
// the claims. It performs NO revocation or token-version check, so a route
// guarded by it alone is not revocable — the composition root layers a
// store-backed verifier (identity.SessionVerifier) for that. It is the natural
// JWT-parsing building block and is used directly by tests that have no DB.
type jwtVerifier struct {
	secret []byte
}

// NewJWTVerifier returns a stateless Verifier that authenticates the HS256
// Bearer access token and maps its claims to a session Principal, without
// consulting any session store. See jwtVerifier for the (deliberate) absence
// of a revocation check.
func NewJWTVerifier(secret []byte) Verifier {
	return jwtVerifier{secret: secret}
}

func (v jwtVerifier) Verify(_ context.Context, r *http.Request) (Principal, bool, error) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return Principal{}, false, nil // no Bearer credential presented: defer
	}
	claims, err := ParseToken(v.secret, token)
	if err != nil {
		// A Bearer token WAS presented but is invalid (bad sig/alg/expired):
		// reject definitively rather than defer, so it can't be masked.
		return Principal{}, false, ErrUnauthorized
	}
	return PrincipalFromClaims(claims), true, nil
}

// PrincipalFromClaims maps validated access-token claims to a session
// Principal. Shared by the stateless jwtVerifier and the store-backed
// identity.SessionVerifier so both build the identical context value.
func PrincipalFromClaims(c Claims) Principal {
	return Principal{
		UserID:      c.UserID,
		WorkspaceID: c.WorkspaceID,
		Role:        c.Role,
		SessionID:   c.SessionID,
		Kind:        KindSession,
	}
}

// UserFromContext returns the authenticated principal, if present.
func UserFromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok
}

// WorkspaceID extracts and parses the caller's workspace id from the principal
// RequireAuth stashed on the request context. On failure it writes the HTTP
// error and returns ok=false so the handler can `return` immediately. Shared
// by every route that scopes work to a workspace.
func WorkspaceID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	p, ok := UserFromContext(r.Context())
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "unauthorized")
		return uuid.Nil, false
	}
	id, err := uuid.Parse(p.WorkspaceID)
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, "bad workspace")
		return uuid.Nil, false
	}
	return id, true
}

// RequireScope rejects (403) a principal that lacks scope. A session principal
// implicitly passes (HasScope returns true); a machine principal must have
// been granted the exact scope. Must run after RequireAuth.
func RequireScope(scope string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := UserFromContext(r.Context())
			if !ok {
				httpx.Error(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			if !p.HasScope(scope) {
				httpx.Error(w, http.StatusForbidden, "insufficient scope")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// VerifiedChecker looks up whether a user has confirmed their email address.
// Satisfied by identity.Store; kept as a tiny interface here so auth doesn't
// import the identity package.
type VerifiedChecker interface {
	IsEmailVerified(ctx context.Context, userID uuid.UUID) (bool, error)
}

// RequireVerified rejects callers whose email isn't verified yet (403
// email_not_verified). Must run after RequireAuth: it reads UserID from the
// principal RequireAuth stashed on the context.
func RequireVerified(c VerifiedChecker) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := UserFromContext(r.Context())
			if !ok {
				httpx.Error(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			uid, err := uuid.Parse(p.UserID)
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

// RequireRole rejects (403) callers whose workspace role ranks below minRole.
// Must run after RequireAuth.
func RequireRole(minRole string) func(http.Handler) http.Handler {
	want, ok := roleRank[minRole]
	if !ok {
		panic("auth.RequireRole: unknown role " + minRole)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := UserFromContext(r.Context())
			if !ok || roleRank[p.Role] < want {
				httpx.Error(w, http.StatusForbidden, "insufficient role")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
