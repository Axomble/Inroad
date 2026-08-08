package identity

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/notify"
)

// newGoogleTestHandler wires a Handler over a Service with the fake Google
// provider, matching newTestHandler's shape.
func newGoogleTestHandler(store *fakeStore, g *fakeGoogle) *Handler {
	svc := NewService(store, 30*24*time.Hour, &fakeSender{}, "https://app.example.test",
		time.Hour, time.Hour, time.Hour, WithGoogleSignIn(g, googleStateSecret))
	return NewHandler(svc, []byte("test-secret-test-secret"), 15*time.Minute, 30*24*time.Hour,
		false, "", nil, nil, nil)
}

// callbackLocation drives the callback handler and returns its status and
// Location header.
func callbackLocation(t *testing.T, h *Handler, query string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/oauth/google/callback?"+query, http.NoBody)
	w := httptest.NewRecorder()
	h.googleSignInCallback(w, req)
	return w.Code, w.Header().Get("Location")
}

// The callback is a browser navigation: it always 302s to the SPA, sets the
// refresh + CSRF cookies, and never puts a token in the URL (which would land in
// browser history and any Referer).
func TestGoogleCallbackSetsCookiesAndRedirects(t *testing.T) {
	store := newFakeStore()
	g := &fakeGoogle{enabled: true, identity: verifiedIdentity()}
	h := newGoogleTestHandler(store, g)

	state := startAndState(t, h.svc, "")
	req := httptest.NewRequest(http.MethodGet, "/oauth/google/callback?code=abc&state="+state, http.NoBody)
	w := httptest.NewRecorder()
	h.googleSignInCallback(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("want 302, got %d: %s", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	if loc != "https://app.example.test"+googleSuccessPath+"?signin=ok" {
		t.Fatalf("unexpected redirect: %q", loc)
	}
	if strings.Contains(loc, "access_token") || strings.Contains(loc, "eyJ") {
		t.Fatalf("redirect URL must carry no token: %q", loc)
	}

	var gotRefresh, gotCSRF bool
	for _, c := range w.Result().Cookies() {
		switch c.Name {
		case refreshCookieName:
			gotRefresh = c.Value != "" && c.HttpOnly
		case auth.CSRFCookieName:
			gotCSRF = c.Value != ""
		}
	}
	if !gotRefresh {
		t.Fatal("want an httpOnly refresh cookie")
	}
	if !gotCSRF {
		t.Fatal("want a CSRF cookie")
	}
}

// Every failure mode is a redirect with a machine-readable reason, never a 5xx and
// never server-side detail in the URL.
func TestGoogleCallbackRedirectsOnFailure(t *testing.T) {
	store := newFakeStore()
	g := &fakeGoogle{enabled: true, identity: verifiedIdentity()}
	h := newGoogleTestHandler(store, g)

	t.Run("user denied consent", func(t *testing.T) {
		code, loc := callbackLocation(t, h, "error=access_denied")
		if code != http.StatusFound || !strings.HasSuffix(loc, "google_error=denied") {
			t.Fatalf("got %d %q", code, loc)
		}
	})

	t.Run("no code at all", func(t *testing.T) {
		code, loc := callbackLocation(t, h, "state=whatever")
		if code != http.StatusFound || !strings.HasSuffix(loc, "google_error=denied") {
			t.Fatalf("got %d %q", code, loc)
		}
	})

	t.Run("forged state", func(t *testing.T) {
		code, loc := callbackLocation(t, h, "code=abc&state=forged")
		if code != http.StatusFound || !strings.HasSuffix(loc, "google_error=bad_state") {
			t.Fatalf("got %d %q", code, loc)
		}
	})

	t.Run("replayed state", func(t *testing.T) {
		state := startAndState(t, h.svc, "")
		if code, _ := callbackLocation(t, h, "code=abc&state="+state); code != http.StatusFound {
			t.Fatalf("first use: got %d", code)
		}
		code, loc := callbackLocation(t, h, "code=abc&state="+state)
		if code != http.StatusFound || !strings.HasSuffix(loc, "google_error=bad_state") {
			t.Fatalf("got %d %q", code, loc)
		}
	})

	t.Run("unverified provider email", func(t *testing.T) {
		unverified := &fakeGoogle{enabled: true, identity: verifiedIdentity()}
		unverified.identity.EmailVerified = false
		uh := newGoogleTestHandler(newFakeStore(), unverified)
		state := startAndState(t, uh.svc, "")
		code, loc := callbackLocation(t, uh, "code=abc&state="+state)
		if code != http.StatusFound || !strings.HasSuffix(loc, "google_error=email_unverified") {
			t.Fatalf("got %d %q", code, loc)
		}
	})

	t.Run("exchange failure leaks no detail", func(t *testing.T) {
		broken := &fakeGoogle{enabled: true, err: errors.New("google said no: internal-detail-abc123")}
		bh := newGoogleTestHandler(newFakeStore(), broken)
		state := startAndState(t, bh.svc, "")
		code, loc := callbackLocation(t, bh, "code=abc&state="+state)
		if code != http.StatusFound || !strings.HasSuffix(loc, "google_error=exchange_failed") {
			t.Fatalf("got %d %q", code, loc)
		}
		if strings.Contains(loc, "internal-detail-abc123") {
			t.Fatalf("redirect leaked server-side detail: %q", loc)
		}
	})
}

// With no Google credentials the start endpoint answers 501, so the SPA can hide
// the button rather than offering a broken redirect.
func TestStartGoogleSignInDisabledReturns501(t *testing.T) {
	h := newGoogleTestHandler(newFakeStore(), &fakeGoogle{enabled: false})
	req := httptest.NewRequest(http.MethodPost, "/oauth/google/start", http.NoBody)
	w := httptest.NewRecorder()
	h.startGoogleSignInJSON(w, req)
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("want 501, got %d: %s", w.Code, w.Body.String())
	}
}

// An absent body is a legitimate plain sign-in (no invite), so it must not 400.
func TestStartGoogleSignInAcceptsEmptyBody(t *testing.T) {
	h := newGoogleTestHandler(newFakeStore(), &fakeGoogle{enabled: true, identity: verifiedIdentity()})
	req := httptest.NewRequest(http.MethodPost, "/oauth/google/start", http.NoBody)
	w := httptest.NewRecorder()
	h.startGoogleSignInJSON(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "auth_url") {
		t.Fatalf("want an auth_url in the body, got %s", w.Body.String())
	}
}

// The `state` parameter must never carry the invite token: it travels through
// Google's servers, browser history, and any Referer, and an invite token is a
// bearer credential granting workspace membership. Only its hash is kept, server-side.
func TestStartGoogleSignInKeepsInviteTokenOutOfTheState(t *testing.T) {
	store := newFakeStore()
	h := newGoogleTestHandler(store, &fakeGoogle{enabled: true, identity: verifiedIdentity()})

	const inviteToken = "invite-token-abcdef123456"
	url, err := h.svc.StartGoogleSignIn(context.Background(), StartGoogleSignInInput{InviteToken: inviteToken})
	if err != nil {
		t.Fatalf("StartGoogleSignIn: %v", err)
	}
	if strings.Contains(url, inviteToken) {
		t.Fatalf("consent URL carries the raw invite token: %q", url)
	}
	if len(store.loginStates) != 1 {
		t.Fatalf("want one persisted login state, got %d", len(store.loginStates))
	}
	for _, s := range store.loginStates {
		if len(s.InviteTokenHash) == 0 {
			t.Fatal("want the invite token's HASH persisted server-side")
		}
		if string(s.InviteTokenHash) == inviteToken {
			t.Fatal("the invite token must be hashed, not stored raw")
		}
	}
}

// A compile-time assertion that the notify.Sender the tests inject is the same
// seam production uses (the fake would otherwise silently drift).
var _ notify.Sender = (*fakeSender)(nil)

// The GET entry point is a top-level navigation: it 302s straight to the provider,
// so the SPA can window.location.assign() it without reading a body.
func TestGetStartRedirectsToProvider(t *testing.T) {
	g := &fakeGoogle{enabled: true, identity: verifiedIdentity()}
	h := newGoogleTestHandler(newFakeStore(), g)

	req := httptest.NewRequest(http.MethodGet, "/oauth/google/start", http.NoBody)
	w := httptest.NewRecorder()
	h.startGoogleSignIn(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("want 302, got %d: %s", w.Code, w.Body.String())
	}
	if loc := w.Header().Get("Location"); !strings.HasPrefix(loc, "https://accounts.google.test/consent") {
		t.Fatalf("want a redirect to the provider, got %q", loc)
	}
}

// On a deployment with no Google credentials the GET entry point still lands the
// user somewhere sensible: the login page, with a reason the SPA has copy for.
// (The JSON entry point answers 501 instead, so a client CAN hide the button.)
func TestGetStartRedirectsToLoginWhenDisabled(t *testing.T) {
	h := newGoogleTestHandler(newFakeStore(), &fakeGoogle{enabled: false})

	req := httptest.NewRequest(http.MethodGet, "/oauth/google/start", http.NoBody)
	w := httptest.NewRecorder()
	h.startGoogleSignIn(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("want 302, got %d", w.Code)
	}
	want := "https://app.example.test/?google_error=disabled"
	if loc := w.Header().Get("Location"); loc != want {
		t.Fatalf("want %q, got %q", want, loc)
	}
}

// A return_to survives the whole round trip and comes back on the SUCCESS redirect
// for the SPA to navigate to once it has a session.
func TestReturnToSurvivesTheRoundTrip(t *testing.T) {
	store := newFakeStore()
	h := newGoogleTestHandler(store, &fakeGoogle{enabled: true, identity: verifiedIdentity()})

	req := httptest.NewRequest(http.MethodGet, "/oauth/google/start?return_to=%2Finbox%3Ftab%3Dunread", http.NoBody)
	w := httptest.NewRecorder()
	h.startGoogleSignIn(w, req)
	state := stateFromLocation(t, w.Header().Get("Location"))

	code, loc := callbackLocation(t, h, "code=abc&state="+state)
	if code != http.StatusFound {
		t.Fatalf("want 302, got %d", code)
	}
	want := "https://app.example.test/auth/google/callback?signin=ok&return_to=%2Finbox%3Ftab%3Dunread"
	if loc != want {
		t.Fatalf("want %q, got %q", want, loc)
	}
}

// stateFromLocation pulls the `state` parameter out of a consent-URL redirect.
func stateFromLocation(t *testing.T, loc string) string {
	t.Helper()
	_, state, ok := strings.Cut(loc, "state=")
	if !ok {
		t.Fatalf("no state in %q", loc)
	}
	return state
}

// An unsafe return_to is DROPPED rather than honored, so the success redirect
// carries no attacker-chosen destination. Full coverage of the predicate itself is
// in TestSafeReturnTo; this proves the flow actually consults it.
func TestUnsafeReturnToIsDropped(t *testing.T) {
	store := newFakeStore()
	h := newGoogleTestHandler(store, &fakeGoogle{enabled: true, identity: verifiedIdentity()})

	req := httptest.NewRequest(http.MethodGet, "/oauth/google/start?return_to=https%3A%2F%2Fevil.example%2Fsteal", http.NoBody)
	w := httptest.NewRecorder()
	h.startGoogleSignIn(w, req)
	state := stateFromLocation(t, w.Header().Get("Location"))

	_, loc := callbackLocation(t, h, "code=abc&state="+state)
	if strings.Contains(loc, "evil.example") {
		t.Fatalf("success redirect carried an off-origin return_to: %q", loc)
	}
	if loc != "https://app.example.test/auth/google/callback?signin=ok" {
		t.Fatalf("unexpected redirect: %q", loc)
	}
}

// "Google gave us no address" and "Google says that address is unverified" are
// different failures with different user-facing copy, so they must not collapse
// into one reason.
func TestNoEmailAndUnverifiedEmailAreDistinctReasons(t *testing.T) {
	noEmail := &fakeGoogle{enabled: true, identity: GoogleIdentity{Subject: "sub-1", EmailVerified: true}}
	nh := newGoogleTestHandler(newFakeStore(), noEmail)
	state := startAndState(t, nh.svc, "")
	if _, loc := callbackLocation(t, nh, "code=abc&state="+state); !strings.HasSuffix(loc, "google_error=no_email") {
		t.Fatalf("want google_error=no_email, got %q", loc)
	}

	unverified := &fakeGoogle{enabled: true, identity: verifiedIdentity()}
	unverified.identity.EmailVerified = false
	uh := newGoogleTestHandler(newFakeStore(), unverified)
	state = startAndState(t, uh.svc, "")
	if _, loc := callbackLocation(t, uh, "code=abc&state="+state); !strings.HasSuffix(loc, "google_error=email_unverified") {
		t.Fatalf("want google_error=email_unverified, got %q", loc)
	}
}

// countingMiddleware records how many requests passed through it, so a test can
// prove which routes a pre-auth guard is actually mounted on.
func countingMiddleware(n *int) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			*n++
			next.ServeHTTP(w, r)
		})
	}
}

// Both START routes are throttled (each writes an oauth_login_states row on an
// unauthenticated request), and the CALLBACK is not (it is the provider redirecting
// a real user's browser back — shedding it would break a legitimate sign-in, and its
// signed single-use state is a stronger guard than a rate limit anyway).
func TestGoogleStartThrottleCoversBothStartRoutesButNotTheCallback(t *testing.T) {
	h := newGoogleTestHandler(newFakeStore(), &fakeGoogle{enabled: true, identity: verifiedIdentity()})

	var hits int
	router := h.Routes(RouteDeps{GoogleStartThrottle: countingMiddleware(&hits)})

	for _, tc := range []struct {
		name, method, path string
		wantHits           int
	}{
		{"GET start is throttled", http.MethodGet, "/oauth/google/start", 1},
		{"POST start is throttled", http.MethodPost, "/oauth/google/start", 2},
		{"callback is NOT throttled", http.MethodGet, "/oauth/google/callback?code=x&state=y", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, http.NoBody)
			router.ServeHTTP(httptest.NewRecorder(), req)
			if hits != tc.wantHits {
				t.Fatalf("throttle hits = %d, want %d", hits, tc.wantHits)
			}
		})
	}
}

// A nil throttle is skipped rather than invoked, so a bare test wiring (and any
// deployment that leaves it unset) still serves the routes.
func TestGoogleStartRoutesWorkWithoutAThrottle(t *testing.T) {
	h := newGoogleTestHandler(newFakeStore(), &fakeGoogle{enabled: true, identity: verifiedIdentity()})
	router := h.Routes(RouteDeps{})

	req := httptest.NewRequest(http.MethodGet, "/oauth/google/start", http.NoBody)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusFound {
		t.Fatalf("want 302 with no throttle wired, got %d: %s", w.Code, w.Body.String())
	}
}
