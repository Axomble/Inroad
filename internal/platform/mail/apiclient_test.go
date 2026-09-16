package mail

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// unroutableEgressIP is TEST-NET-3 (RFC 5737): reserved for documentation and
// never assigned to a host interface, so binding it as a SOURCE address always
// fails at the bind with EADDRNOTAVAIL. That failure IS the observation these
// tests make. A transport that silently drops LocalAddr — http.DefaultTransport,
// which is what oauth2.NewClient falls back to — reaches the loopback server
// instead, so "the request succeeded" and "the binding was applied" are mutually
// exclusive outcomes here. Nothing weaker distinguishes them: the bound source
// address is not readable back off an *http.Transport.
const unroutableEgressIP = "203.0.113.1"

// okServer is a loopback HTTP server that answers everything 200 and records the
// Authorization header it was sent.
func okServer(t *testing.T, auth *string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth != nil {
			*auth = r.Header.Get("Authorization")
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// getThrough issues one GET through c and closes the body, returning the error.
func getThrough(t *testing.T, c *http.Client, url string) error {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, http.NoBody)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

// TestAPIHTTPClientBindsTheConfiguredEgressIP is the regression test for the
// silent no-op: the provider API legs built their clients with oauth2.NewClient,
// which uses http.DefaultTransport, so INROAD_WORKER_EGRESS_IP never reached
// Gmail or Graph traffic at all.
//
// Both halves matter. The nil case is the common one (no egress IP configured,
// every self-hoster) and must keep using the OS default route; the bound case is
// the fleet one and must actually bind.
func TestAPIHTTPClientBindsTheConfiguredEgressIP(t *testing.T) {
	srv := okServer(t, nil)

	// Control: an unconfigured egress IP must behave exactly as before.
	if err := getThrough(t, newAPIHTTPClient(nil), srv.URL); err != nil {
		t.Fatalf("unbound client must reach the server over the OS default route: %v", err)
	}

	// Bound to an address this host cannot own. If LocalAddr were dropped, this
	// would succeed exactly like the control above.
	err := getThrough(t, newAPIHTTPClient(&net.TCPAddr{IP: net.ParseIP(unroutableEgressIP)}), srv.URL)
	if err == nil {
		t.Fatal("client bound to an unassignable source address reached the server: LocalAddr was dropped")
	}
	if !strings.Contains(err.Error(), unroutableEgressIP) {
		t.Fatalf("dial failed for some other reason than the source bind: %v", err)
	}
}

// TestAPIHTTPClientSetsChosenTimeouts guards the second half of the defect:
// http.DefaultTransport sets neither TLSHandshakeTimeout that we chose nor any
// ResponseHeaderTimeout at all, and http.Client.Timeout alone does not bound a
// provider that accepts the connection and then never sends a header.
func TestAPIHTTPClientSetsChosenTimeouts(t *testing.T) {
	c := newAPIHTTPClient(nil)
	if c.Timeout != apiRequestTimeout || c.Timeout <= 0 {
		t.Fatalf("http.Client.Timeout = %v, want %v", c.Timeout, apiRequestTimeout)
	}
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport is %T, want *http.Transport", c.Transport)
	}
	if tr.TLSHandshakeTimeout != apiTLSHandshakeTimeout || tr.TLSHandshakeTimeout <= 0 {
		t.Fatalf("TLSHandshakeTimeout = %v, want %v", tr.TLSHandshakeTimeout, apiTLSHandshakeTimeout)
	}
	if tr.ResponseHeaderTimeout != apiResponseHeaderTimeout || tr.ResponseHeaderTimeout <= 0 {
		t.Fatalf("ResponseHeaderTimeout = %v, want %v (a body that never arrives is not covered by Client.Timeout alone)",
			tr.ResponseHeaderTimeout, apiResponseHeaderTimeout)
	}
	// Setting DialContext disables net/http's automatic HTTP/2 upgrade unless
	// this is set. Both provider APIs speak h2; silently dropping to HTTP/1.1
	// would be a second invisible regression shipped by the fix for the first.
	if !tr.ForceAttemptHTTP2 {
		t.Fatal("ForceAttemptHTTP2 is false: a custom DialContext silently disables HTTP/2")
	}
	// A self-hoster behind an outbound HTTP proxy relies on this;
	// http.DefaultTransport sets it and dropping it would break them.
	if tr.Proxy == nil {
		t.Fatal("Proxy is nil: outbound proxy configuration (HTTPS_PROXY) would be ignored")
	}
}

// TestBearerClientKeepsTheEgressBindingAndTheToken proves the oauth2 wrapper
// preserves what we built rather than replacing it. oauth2.NewClient reads the
// base client out of the context (oauth2.HTTPClient) and copies its Transport,
// Timeout, Jar and CheckRedirect onto the client it returns; if the base is not
// in the context it falls back to http.DefaultClient and every timeout and the
// egress binding are lost. That fallback is the bug, so this asserts the
// binding survives the wrap AND that the token source still signs the request.
func TestBearerClientKeepsTheEgressBindingAndTheToken(t *testing.T) {
	var gotAuth string
	srv := okServer(t, &gotAuth)
	ctx := context.Background()

	c := bearerClient(ctx, newAPIHTTPClient(nil), "tok-123")
	if err := getThrough(t, c, srv.URL); err != nil {
		t.Fatalf("bearer client over an unbound base: %v", err)
	}
	if gotAuth != "Bearer tok-123" {
		t.Fatalf("Authorization = %q, want %q: the oauth2 token source was lost", gotAuth, "Bearer tok-123")
	}
	if c.Timeout != apiRequestTimeout {
		t.Fatalf("wrapped client Timeout = %v, want %v: oauth2.NewClient did not carry the base client's timeout", c.Timeout, apiRequestTimeout)
	}

	bound := bearerClient(ctx, newAPIHTTPClient(&net.TCPAddr{IP: net.ParseIP(unroutableEgressIP)}), "tok-123")
	err := getThrough(t, bound, srv.URL)
	if err == nil {
		t.Fatal("bearer client over an egress-bound base reached the server: the oauth2 wrap discarded our transport")
	}
	if !strings.Contains(err.Error(), unroutableEgressIP) {
		t.Fatalf("dial failed for some other reason than the source bind: %v", err)
	}
}
