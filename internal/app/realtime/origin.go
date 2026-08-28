package realtime

import (
	"net/http"
	"strings"
)

// originAllowed reports whether a handshake's Origin header may open a socket.
//
// This check is MANDATORY, and it is the one place in this codebase where
// "we're same-origin, so it's fine" is actively wrong (spec §3). A WebSocket
// handshake is NOT subject to the same-origin policy: the browser sends it
// cross-origin with the user's cookies and lets the server decide. There is no
// CORS middleware in this repo to lean on either. Without an explicit allowlist,
// any site the user happened to visit could open an authenticated socket onto
// their workspace and read its whole event stream.
//
// allowed is the configured origin set (cfg.RPOrigin — the fully-qualified
// origin the browser must present, already derived from INROAD_PUBLIC_URL and
// already used to bind passkey ceremonies). Reusing it means an operator who has
// configured passkeys correctly has configured this correctly.
//
// A MISSING Origin is allowed. That is deliberate and worth stating, because it
// looks like a hole: browsers always send Origin on a WebSocket handshake, so an
// absent one means a non-browser client (a CLI, a test, a server-side consumer),
// which is not what this check defends against — the attack is a *browser* being
// steered by another site's script. Such a client still needs a valid,
// unexpired, unspent ticket and a live session. If the deployment wants to
// exclude non-browser clients entirely, that is a different control.
//
// Comparison is exact after lowercasing scheme and host, with no suffix or
// substring matching: `https://evil-inroad.com` must never satisfy an allowlist
// containing `https://inroad.com`, and a `strings.HasSuffix` check would let it.
func originAllowed(origin string, allowed []string) bool {
	if origin == "" {
		return true
	}
	got := normalizeOrigin(origin)
	if got == "" {
		return false
	}
	for _, want := range allowed {
		if want == "" {
			continue
		}
		if normalizeOrigin(want) == got {
			return true
		}
	}
	return false
}

// normalizeOrigin lowercases an origin and strips a trailing slash, so a
// configured "https://App.Example.com/" matches a browser's
// "https://app.example.com". It deliberately does NOT parse and re-serialize:
// anything that is not a plain scheme://host[:port] should fail the comparison
// rather than be coerced into passing it.
func normalizeOrigin(origin string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(origin), "/"))
}

// checkOrigin adapts originAllowed to the gorilla/websocket Upgrader hook, whose
// signature takes the whole request.
func checkOrigin(allowed []string) func(*http.Request) bool {
	return func(r *http.Request) bool {
		return originAllowed(r.Header.Get("Origin"), allowed)
	}
}
