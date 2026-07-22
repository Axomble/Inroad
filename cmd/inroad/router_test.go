package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/app/identity"
)

// testSecret is a fixed HS256 key for minting/verifying access tokens in these
// router tests; it never leaves the test binary.
var testSecret = []byte("router-test-secret-router-test-secret")

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// okHandler is a bare handler that always 200s. Used to stand in for a domain
// route that does NOT apply any auth of its own -- the whole point of the
// deny-by-default group is that such a route is still protected.
func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

// bearerFor mints a valid access token for a throwaway workspace.
func bearerFor(t *testing.T) string {
	t.Helper()
	tok, err := auth.IssueToken(testSecret, auth.Claims{
		UserID:      "11111111-1111-1111-1111-111111111111",
		WorkspaceID: "22222222-2222-2222-2222-222222222222",
		Role:        "member",
		SessionID:   "33333333-3333-3333-3333-333333333333",
	}, time.Minute)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	return "Bearer " + tok
}

func do(t *testing.T, r http.Handler, method, path, authHeader string, headers map[string]string, cookies ...*http.Cookie) int {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Code
}

// TestBuildRouterProtectedGroupRejectsAnonymous asserts the protected group
// wraps its mounts in RequireAuth: a mounted route with NO bearer token 401s.
func TestBuildRouterProtectedGroupRejectsAnonymous(t *testing.T) {
	mb := chi.NewRouter()
	mb.Get("/", okHandler().ServeHTTP)

	r := buildRouter(discardLogger(), testSecret,
		nil,
		[]mount{{pattern: "/api/v1/mailboxes", handler: mb}},
	)

	if code := do(t, r, http.MethodGet, "/api/v1/mailboxes/", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("no token: got %d, want 401", code)
	}
	if code := do(t, r, http.MethodGet, "/api/v1/mailboxes/", bearerFor(t), nil); code != http.StatusOK {
		t.Fatalf("valid token: got %d, want 200", code)
	}
}

// TestBuildRouterFailsSafeForNewRoutes is the core of the task: a brand-new
// route mounted under the protected group WITHOUT any local auth still 401s
// without a token. New domains inherit protection by default; forgetting a
// per-domain middleware can no longer expose a route.
func TestBuildRouterFailsSafeForNewRoutes(t *testing.T) {
	// A future domain, mounted with zero auth wiring of its own.
	brandNew := chi.NewRouter()
	brandNew.Get("/", okHandler().ServeHTTP)

	r := buildRouter(discardLogger(), testSecret,
		nil,
		[]mount{{pattern: "/api/v1/brand-new", handler: brandNew}},
	)

	if code := do(t, r, http.MethodGet, "/api/v1/brand-new/", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("fail-safe: unauthenticated new route got %d, want 401", code)
	}
	if code := do(t, r, http.MethodGet, "/api/v1/brand-new/", bearerFor(t), nil); code != http.StatusOK {
		t.Fatalf("fail-safe: authenticated new route got %d, want 200", code)
	}
}

// TestBuildRouterPublicGroupNeedsNoToken asserts public mounts and /healthz are
// reachable without an access token.
func TestBuildRouterPublicGroupNeedsNoToken(t *testing.T) {
	pub := chi.NewRouter()
	pub.Post("/register", okHandler().ServeHTTP)
	pub.Post("/login", okHandler().ServeHTTP)

	r := buildRouter(discardLogger(), testSecret,
		[]mount{{pattern: "/api/v1/auth", handler: pub}},
		nil,
	)

	if code := do(t, r, http.MethodGet, "/healthz", "", nil); code != http.StatusOK {
		t.Fatalf("healthz: got %d, want 200", code)
	}
	if code := do(t, r, http.MethodPost, "/api/v1/auth/register", "", nil); code != http.StatusOK {
		t.Fatalf("register: got %d, want 200", code)
	}
	if code := do(t, r, http.MethodPost, "/api/v1/auth/login", "", nil); code != http.StatusOK {
		t.Fatalf("login: got %d, want 200", code)
	}
}

// TestBuildRouterPublicCSRFRouteNeedsCSRFNotBearer mirrors the refresh/logout
// contract: those endpoints are CSRF-gated but must work when the access token
// is absent/expired, so they belong in the public group (no RequireAuth), each
// applying RequireCSRF locally.
func TestBuildRouterPublicCSRFRouteNeedsCSRFNotBearer(t *testing.T) {
	pub := chi.NewRouter()
	pub.With(auth.RequireCSRF).Post("/refresh", okHandler().ServeHTTP)

	r := buildRouter(discardLogger(), testSecret,
		[]mount{{pattern: "/api/v1/auth", handler: pub}},
		nil,
	)

	// Missing CSRF -> 403 (not 401): the public group added no access-token gate.
	if code := do(t, r, http.MethodPost, "/api/v1/auth/refresh", "", nil); code != http.StatusForbidden {
		t.Fatalf("refresh without csrf: got %d, want 403", code)
	}
	// Valid double-submit CSRF, no bearer -> passes.
	const csrf = "csrf-value"
	code := do(t, r, http.MethodPost, "/api/v1/auth/refresh", "",
		map[string]string{auth.CSRFHeaderName: csrf},
		&http.Cookie{Name: auth.CSRFCookieName, Value: csrf},
	)
	if code != http.StatusOK {
		t.Fatalf("refresh with csrf and no bearer: got %d, want 200", code)
	}
}

// TestBuildRouterIdentityMeStaysGuarded locks the one documented deny-by-default
// deviation: identity is mounted in the PUBLIC group (register/login/refresh/
// logout must work without a valid access token) but self-guards /me,
// /logout-all and /switch-workspace with its own inner RequireAuth. That inner
// guard is the ONLY thing protecting those endpoints -- if a future refactor
// drops it (as this task dropped the local guard from the business domains),
// /me would be reachable unauthenticated. This test exercises the real
// identity.Routes so such a regression fails here. A zero-value Handler is safe:
// RequireAuth rejects the tokenless request before any handler (or its nil
// service) is invoked.
func TestBuildRouterIdentityMeStaysGuarded(t *testing.T) {
	h := &identity.Handler{}

	r := buildRouter(discardLogger(), testSecret,
		[]mount{{pattern: "/api/v1/auth", handler: h.Routes(testSecret)}},
		nil,
	)

	if code := do(t, r, http.MethodGet, "/api/v1/auth/me", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("identity /me without token: got %d, want 401 (inner self-guard removed?)", code)
	}
}
