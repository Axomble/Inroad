package ai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inroad/inroad/internal/platform/mail"
)

// gatewayServer serves an OpenAI-compatible GET /models with the given body,
// capturing the Authorization header it saw.
func gatewayServer(t *testing.T, body string, gotAuth *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		*gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
}

// TestDiscoverOpenRouterStyleMetadata proves the rich-gateway path: names,
// labels, context windows, and per-token pricing strings mapped to per-MTok.
func TestDiscoverOpenRouterStyleMetadata(t *testing.T) {
	var auth string
	ts := gatewayServer(t, `{"data":[
		{"id":"anthropic/claude-sonnet-5","name":"Claude Sonnet 5","context_length":1000000,
		 "top_provider":{"max_completion_tokens":128000},
		 "pricing":{"prompt":"0.000003","completion":"0.000015"}},
		{"id":"meta-llama/llama-3.3-70b","context_length":131072}
	]}`, &auth)
	defer ts.Close()

	// The httptest server listens on 127.0.0.1 — loopback — so discovery only
	// works with the operator private-host opt-in, proving the dial itself is
	// guarded.
	d := NewHTTPDiscoverer(true, 0)
	res, err := d.Discover(context.Background(), DiscoverRequest{
		Kind:        KindOpenAICompatible,
		Config:      map[string]string{"base_url": ts.URL + "/v1"},
		Credentials: Credentials{APIKey: "sk-or-testkey-123"},
	})
	if err != nil || !res.Supported {
		t.Fatalf("Discover: %+v, %v", res, err)
	}
	if auth != "Bearer sk-or-testkey-123" {
		t.Fatalf("auth header wrong: %q", auth)
	}
	if len(res.Models) != 2 {
		t.Fatalf("want 2 models, got %+v", res.Models)
	}
	rich := res.Models[0]
	if rich.Name != "anthropic/claude-sonnet-5" || rich.Label != "Claude Sonnet 5" ||
		rich.ContextWindowTokens != 1000000 || rich.MaxOutputTokens != 128000 {
		t.Fatalf("rich entry wrong: %+v", rich)
	}
	if rich.InputCostPerMTok == nil || *rich.InputCostPerMTok != 3 ||
		rich.OutputCostPerMTok == nil || *rich.OutputCostPerMTok != 15 {
		t.Fatalf("pricing mapping wrong: %+v", rich)
	}
	if res.Models[1].InputCostPerMTok != nil {
		t.Fatalf("absent pricing must be nil, not zero: %+v", res.Models[1])
	}
}

// TestDiscoverBareIDs proves a minimal endpoint (plain OpenAI list shape,
// ids only, no auth configured) yields name-only entries with no auth header.
func TestDiscoverBareIDs(t *testing.T) {
	var auth string
	ts := gatewayServer(t, `{"data":[{"id":"llama3.2"},{"id":"qwen2.5-coder"}]}`, &auth)
	defer ts.Close()

	d := NewHTTPDiscoverer(true, 0)
	res, err := d.Discover(context.Background(), DiscoverRequest{
		Kind:   KindOpenAICompatible,
		Config: map[string]string{"base_url": ts.URL + "/v1"},
	})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if auth != "" {
		t.Fatalf("keyless door must send no Authorization header, got %q", auth)
	}
	if len(res.Models) != 2 || res.Models[0].Name != "llama3.2" || res.Models[0].ContextWindowTokens != 0 {
		t.Fatalf("bare-id mapping wrong: %+v", res.Models)
	}
}

// TestDiscoverSSRFRejectedWithoutOptIn proves the DIAL is guarded: the same
// loopback gateway that works with the opt-in is refused without it.
func TestDiscoverSSRFRejectedWithoutOptIn(t *testing.T) {
	var auth string
	ts := gatewayServer(t, `{"data":[]}`, &auth)
	defer ts.Close()

	d := NewHTTPDiscoverer(false, 0)
	_, err := d.Discover(context.Background(), DiscoverRequest{
		Kind:   KindOpenAICompatible,
		Config: map[string]string{"base_url": ts.URL + "/v1"},
	})
	if err == nil || !errors.Is(err, mail.ErrHostNotPermitted) {
		t.Fatalf("loopback dial without opt-in must be ErrHostNotPermitted, got %v", err)
	}
}

// TestDiscoverTimeout proves a hung endpoint fails within the configured
// timeout instead of blocking the request.
func TestDiscoverTimeout(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
	}))
	defer ts.Close()

	d := NewHTTPDiscoverer(true, 100*time.Millisecond)
	start := time.Now()
	_, err := d.Discover(context.Background(), DiscoverRequest{
		Kind:   KindOpenAICompatible,
		Config: map[string]string{"base_url": ts.URL + "/v1"},
	})
	if err == nil {
		t.Fatal("hung endpoint must error")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("timeout not enforced, took %v", elapsed)
	}
}

// TestDiscoverErrorStatusNeverEchoesBody proves a provider error body (which
// can reflect the key or URL) is not copied into our error.
func TestDiscoverErrorStatusNeverEchoesBody(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"invalid key sk-or-SECRET"}`, http.StatusUnauthorized)
	}))
	defer ts.Close()

	d := NewHTTPDiscoverer(true, 0)
	_, err := d.Discover(context.Background(), DiscoverRequest{
		Kind:   KindOpenAICompatible,
		Config: map[string]string{"base_url": ts.URL + "/v1"},
	})
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("error must not echo the provider body: %v", err)
	}
}

func TestDiscoverDoesNotFollowRedirectsWithCredentials(t *testing.T) {
	var redirected bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/target" {
			redirected = true
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Redirect(w, r, "/target", http.StatusFound)
	}))
	defer ts.Close()

	d := NewHTTPDiscoverer(true, 0)
	_, err := d.Discover(context.Background(), DiscoverRequest{
		Kind: KindOpenAICompatible, Config: map[string]string{"base_url": ts.URL + "/v1"},
		Credentials: Credentials{APIKey: "sk-secret-123456"},
	})
	if err == nil || redirected {
		t.Fatalf("redirect must fail without reaching its target: redirected=%v err=%v", redirected, err)
	}
}

func TestDiscoverRejectsOversizedResponses(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", discoverMaxBytes+1)))
	}))
	defer ts.Close()

	d := NewHTTPDiscoverer(true, 0)
	_, err := d.Discover(context.Background(), DiscoverRequest{
		Kind: KindOpenAICompatible, Config: map[string]string{"base_url": ts.URL + "/v1"},
	})
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized response must be rejected, got %v", err)
	}
}

// TestDiscoverUnsupportedKinds proves the A1 posture for cloud kinds: a clean
// supported=false, never an error.
func TestDiscoverUnsupportedKinds(t *testing.T) {
	d := NewHTTPDiscoverer(false, 0)
	for _, kind := range []string{KindBedrock, KindVertexAnthropic, KindVertexGoogle} {
		res, err := d.Discover(context.Background(), DiscoverRequest{Kind: kind})
		if err != nil || res.Supported || len(res.Models) != 0 {
			t.Fatalf("%s: want supported=false no models, got %+v, %v", kind, res, err)
		}
	}
	if _, err := d.Discover(context.Background(), DiscoverRequest{Kind: "nope"}); err == nil {
		t.Fatal("unknown kind must error")
	}
}

// TestDiscoverAzureDeployments proves the deployments list maps deployment id
// → name and underlying model → label, with the api-key header attached.
func TestDiscoverAzureDeployments(t *testing.T) {
	var gotKey, gotPath, gotQuery string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("api-key")
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"data":[{"id":"prod-gpt","model":"gpt-5.2"}]}`))
	}))
	defer ts.Close()

	d := NewHTTPDiscoverer(true, 0)
	res, err := d.Discover(context.Background(), DiscoverRequest{
		Kind:        KindAzureOpenAI,
		Config:      map[string]string{"endpoint": ts.URL, "api_version": "2024-10-21"},
		Credentials: Credentials{APIKey: "azure-key-123456"},
	})
	if err != nil || !res.Supported {
		t.Fatalf("Discover: %+v, %v", res, err)
	}
	if gotKey != "azure-key-123456" || gotPath != "/openai/deployments" || gotQuery != "api-version=2024-10-21" {
		t.Fatalf("request wrong: key=%q path=%q query=%q", gotKey, gotPath, gotQuery)
	}
	if len(res.Models) != 1 || res.Models[0].Name != "prod-gpt" || res.Models[0].Label != "gpt-5.2" {
		t.Fatalf("deployment mapping wrong: %+v", res.Models)
	}
}
