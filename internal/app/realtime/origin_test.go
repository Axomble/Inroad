package realtime

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

var allowed = []string{"https://app.example.com"}

func TestOriginAllowed_AcceptsTheConfiguredOrigin(t *testing.T) {
	if !originAllowed("https://app.example.com", allowed) {
		t.Error("the configured origin was refused")
	}
}

// Case-insensitive on scheme and host, because a browser may normalise
// differently from however an operator typed INROAD_PUBLIC_URL.
func TestOriginAllowed_IsCaseInsensitiveAndIgnoresATrailingSlash(t *testing.T) {
	for _, origin := range []string{
		"HTTPS://APP.EXAMPLE.COM",
		"https://App.Example.com",
		"https://app.example.com/",
	} {
		if !originAllowed(origin, []string{"https://App.Example.com/"}) {
			t.Errorf("originAllowed(%q) = false, want true", origin)
		}
	}
}

// THE test that matters. A WebSocket handshake is not subject to the same-origin
// policy, so this allowlist is the only thing stopping another site opening an
// authenticated socket onto a user's workspace. Each of these is a near-miss a
// substring, prefix or suffix comparison would wrongly accept.
func TestOriginAllowed_RefusesLookalikeOrigins(t *testing.T) {
	for _, origin := range []string{
		"https://evil-app.example.com",     // subdomain-ish prefix
		"https://app.example.com.evil.com", // suffix attack: HasPrefix would pass
		"https://evilapp.example.com",
		"http://app.example.com",          // scheme downgrade
		"https://app.example.com:8443",    // different port is a different origin
		"https://app.example.com@evil.io", // userinfo confusion
		"null",                            // sandboxed iframe / file://
		"https://sub.app.example.com",
		"app.example.com", // no scheme
	} {
		if originAllowed(origin, allowed) {
			t.Errorf("originAllowed(%q) = true, want false — a cross-origin socket would be allowed", origin)
		}
	}
}

// An empty allowlist must refuse every browser origin rather than fall open.
// This is the misconfiguration case: RPOrigin unset because PUBLIC_URL was not
// set either. Failing closed means the socket does not work; failing open means
// any site can read the workspace.
func TestOriginAllowed_EmptyAllowlistRefusesEveryBrowserOrigin(t *testing.T) {
	for _, allowlist := range [][]string{nil, {}, {""}} {
		if originAllowed("https://app.example.com", allowlist) {
			t.Errorf("allowlist %#v accepted an origin — misconfiguration must fail closed", allowlist)
		}
	}
}

// A MISSING Origin is allowed, deliberately: browsers always send one on a WS
// handshake, so its absence means a non-browser client (CLI, test, server-side
// consumer) — which is not the threat this check addresses. Such a client still
// needs a valid, unexpired, unspent ticket and a live session.
func TestOriginAllowed_AllowsAMissingOrigin(t *testing.T) {
	if !originAllowed("", allowed) {
		t.Error("a missing Origin was refused; see the doc comment for why it is allowed")
	}
	// ...including when nothing is configured, since there is no browser to
	// defend against in that case either.
	if !originAllowed("", nil) {
		t.Error("a missing Origin was refused with an empty allowlist")
	}
}

func TestOriginAllowed_AcceptsAnyEntryInAMultiOriginAllowlist(t *testing.T) {
	list := []string{"https://app.example.com", "https://admin.example.com"}
	for _, origin := range list {
		if !originAllowed(origin, list) {
			t.Errorf("originAllowed(%q) = false, want true", origin)
		}
	}
	if originAllowed("https://other.example.com", list) {
		t.Error("an origin outside the list was accepted")
	}
}

// checkOrigin is what the Upgrader actually calls, so assert the adapter reads
// the header rather than only testing the pure function beneath it.
func TestCheckOrigin_ReadsTheOriginHeader(t *testing.T) {
	check := checkOrigin(allowed)

	ok := httptest.NewRequest(http.MethodGet, "/ws", http.NoBody)
	ok.Header.Set("Origin", "https://app.example.com")
	if !check(ok) {
		t.Error("the configured origin was refused through checkOrigin")
	}

	bad := httptest.NewRequest(http.MethodGet, "/ws", http.NoBody)
	bad.Header.Set("Origin", "https://evil.example.com")
	if check(bad) {
		t.Error("a foreign origin was accepted through checkOrigin")
	}
}
