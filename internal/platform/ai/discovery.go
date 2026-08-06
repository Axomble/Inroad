package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/inroad/inroad/internal/platform/mail"
)

const (
	// discoverTimeout bounds one discovery round trip.
	discoverTimeout = 10 * time.Second
	// discoverMaxBytes caps the response body read.
	discoverMaxBytes = 8 << 20
	// discoverMaxModels caps how many candidates one call returns.
	discoverMaxModels = 500
)

// DiscoverRequest carries everything a discovery dial needs. Credentials are
// the already-unsealed blob; they exist only for the duration of the call and
// are never logged.
type DiscoverRequest struct {
	Kind        string
	Config      map[string]string
	Credentials Credentials
}

// DiscoveredModel is one candidate the provider's list endpoint reported.
// Only Name is guaranteed; everything else is best-effort (bare-id endpoints
// yield name-only entries). The endpoint RETURNS candidates — persistence is
// a separate, explicit POST /ai/models per chosen model.
type DiscoveredModel struct {
	Name                string   `json:"name"`
	Label               string   `json:"label,omitempty"`
	ContextWindowTokens int      `json:"context_window_tokens,omitempty"`
	MaxOutputTokens     int      `json:"max_output_tokens,omitempty"`
	InputCostPerMTok    *float64 `json:"input_cost_per_mtok,omitempty"`
	OutputCostPerMTok   *float64 `json:"output_cost_per_mtok,omitempty"`
}

// DiscoveryResult is the outcome of one discovery call. Supported=false means
// this KIND has no A1 discovery path (bedrock/vertex arrive with the A2 SDK
// runtime) — not an error; manual model entry always works.
type DiscoveryResult struct {
	Supported bool
	Models    []DiscoveredModel
}

// HTTPDiscoverer performs best-effort model-list discovery over plain HTTP —
// deliberately no provider SDKs in A1. Every dial goes through the SSRF-
// guarded transport (mail.GuardedDialContext): fixed provider hosts pass it
// trivially; user-supplied base_url/endpoint hosts are re-vetted at dial time
// with the same private-host gating as at write time.
type HTTPDiscoverer struct {
	client *http.Client
}

// NewHTTPDiscoverer builds a discoverer whose transport enforces the SSRF
// policy. allowPrivate is the operator opt-in for private/loopback targets
// (INROAD_AI_ALLOW_PRIVATE_BASE_URL). timeout <= 0 selects the default.
func NewHTTPDiscoverer(allowPrivate bool, timeout time.Duration) *HTTPDiscoverer {
	if timeout <= 0 {
		timeout = discoverTimeout
	}
	return &HTTPDiscoverer{client: &http.Client{
		Timeout:   timeout,
		Transport: &http.Transport{DialContext: mail.GuardedDialContext(allowPrivate)},
		// Never forward provider credentials through a redirect. Operators must
		// configure the canonical endpoint explicitly.
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

// Discover lists the models a provider door can serve, per kind:
//
//	openai_compatible  GET {base_url}/models          (OpenAI list protocol)
//	openai             GET https://api.openai.com/v1/models
//	anthropic          GET https://api.anthropic.com/v1/models
//	google             GET generativelanguage.googleapis.com/v1beta/models
//	azure_openai       GET {endpoint}/openai/deployments?api-version=…
//	bedrock, vertex_*  Supported=false (A2's SDK runtime brings these)
func (d *HTTPDiscoverer) Discover(ctx context.Context, req DiscoverRequest) (DiscoveryResult, error) {
	switch req.Kind {
	case KindOpenAICompatible:
		base := strings.TrimSuffix(req.Config["base_url"], "/")
		models, err := d.openAIList(ctx, base+"/models", bearerAuth(req.Credentials.APIKey))
		return DiscoveryResult{Supported: true, Models: models}, err
	case KindOpenAI:
		models, err := d.openAIList(ctx, "https://api.openai.com/v1/models", bearerAuth(req.Credentials.APIKey))
		return DiscoveryResult{Supported: true, Models: models}, err
	case KindAnthropic:
		models, err := d.anthropicList(ctx, req.Credentials.APIKey)
		return DiscoveryResult{Supported: true, Models: models}, err
	case KindGoogle:
		models, err := d.googleList(ctx, req.Credentials.APIKey)
		return DiscoveryResult{Supported: true, Models: models}, err
	case KindAzureOpenAI:
		models, err := d.azureDeployments(ctx, req.Config["endpoint"], req.Config["api_version"], req.Credentials.APIKey)
		return DiscoveryResult{Supported: true, Models: models}, err
	case KindBedrock, KindVertexAnthropic, KindVertexGoogle:
		return DiscoveryResult{Supported: false, Models: []DiscoveredModel{}}, nil
	default:
		return DiscoveryResult{}, fmt.Errorf("ai: discover: unknown kind %q", req.Kind)
	}
}

// header is one request header to attach; nil auth means an unauthenticated
// call (an Ollama without a key).
type header struct{ name, value string }

func bearerAuth(apiKey string) []header {
	if apiKey == "" {
		return nil
	}
	return []header{{"Authorization", "Bearer " + apiKey}}
}

// getJSON performs one guarded GET and decodes the body (size-capped) into v.
func (d *HTTPDiscoverer) getJSON(ctx context.Context, rawURL string, headers []header, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, http.NoBody)
	if err != nil {
		return fmt.Errorf("ai: discover: build request: %w", err)
	}
	for _, h := range headers {
		req.Header.Set(h.name, h.value)
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("ai: discover: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		// The body may carry provider error prose; never echo it verbatim
		// (could reflect the URL/key context) — the status is the diagnosis.
		return fmt.Errorf("ai: discover: endpoint returned %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, discoverMaxBytes+1))
	if err != nil {
		return fmt.Errorf("ai: discover: read response: %w", err)
	}
	if len(body) > discoverMaxBytes {
		return fmt.Errorf("ai: discover: response exceeds %d bytes", discoverMaxBytes)
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("ai: discover: endpoint did not return a model list: %w", err)
	}
	return nil
}

// openAIList speaks the OpenAI-compatible GET /models protocol, mapping the
// optional metadata gateways add: OpenRouter reports name, context_length,
// top_provider.max_completion_tokens, and per-TOKEN pricing strings (mapped
// to per-MTok); bare endpoints yield name-only entries.
func (d *HTTPDiscoverer) openAIList(ctx context.Context, listURL string, auth []header) ([]DiscoveredModel, error) {
	var doc struct {
		Data []struct {
			ID            string `json:"id"`
			Name          string `json:"name"`
			ContextLength int    `json:"context_length"`
			TopProvider   struct {
				MaxCompletionTokens int `json:"max_completion_tokens"`
			} `json:"top_provider"`
			Pricing struct {
				Prompt     string `json:"prompt"`
				Completion string `json:"completion"`
			} `json:"pricing"`
		} `json:"data"`
	}
	if err := d.getJSON(ctx, listURL, auth, &doc); err != nil {
		return nil, err
	}
	out := make([]DiscoveredModel, 0, min(len(doc.Data), discoverMaxModels))
	for _, m := range doc.Data {
		if m.ID == "" {
			continue
		}
		if len(out) == discoverMaxModels {
			break
		}
		out = append(out, DiscoveredModel{
			Name:                m.ID,
			Label:               m.Name,
			ContextWindowTokens: m.ContextLength,
			MaxOutputTokens:     m.TopProvider.MaxCompletionTokens,
			InputCostPerMTok:    perTokenToPerMTok(m.Pricing.Prompt),
			OutputCostPerMTok:   perTokenToPerMTok(m.Pricing.Completion),
		})
	}
	return out, nil
}

// perTokenToPerMTok converts a gateway's per-token USD price string ("0.000003")
// to per-million-tokens; unparsable or absent → nil (unknown, not zero).
func perTokenToPerMTok(s string) *float64 {
	if s == "" {
		return nil
	}
	perToken, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(perToken) || math.IsInf(perToken, 0) || perToken < 0 {
		return nil
	}
	v := perToken * 1_000_000
	return &v
}

// anthropicList reads Anthropic's fixed-host GET /v1/models (first page).
func (d *HTTPDiscoverer) anthropicList(ctx context.Context, apiKey string) ([]DiscoveredModel, error) {
	var doc struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
	headers := []header{{"x-api-key", apiKey}, {"anthropic-version", "2023-06-01"}}
	if err := d.getJSON(ctx, "https://api.anthropic.com/v1/models?limit=100", headers, &doc); err != nil {
		return nil, err
	}
	out := make([]DiscoveredModel, 0, len(doc.Data))
	for _, m := range doc.Data {
		if m.ID == "" || len(out) == discoverMaxModels {
			continue
		}
		out = append(out, DiscoveredModel{Name: m.ID, Label: m.DisplayName})
	}
	return out, nil
}

// googleList reads AI Studio's fixed-host models.list.
func (d *HTTPDiscoverer) googleList(ctx context.Context, apiKey string) ([]DiscoveredModel, error) {
	var doc struct {
		Models []struct {
			Name             string `json:"name"` // "models/gemini-…"
			DisplayName      string `json:"displayName"`
			InputTokenLimit  int    `json:"inputTokenLimit"`
			OutputTokenLimit int    `json:"outputTokenLimit"`
		} `json:"models"`
	}
	headers := []header{{"x-goog-api-key", apiKey}}
	if err := d.getJSON(ctx, "https://generativelanguage.googleapis.com/v1beta/models", headers, &doc); err != nil {
		return nil, err
	}
	out := make([]DiscoveredModel, 0, len(doc.Models))
	for _, m := range doc.Models {
		name := strings.TrimPrefix(m.Name, "models/")
		if name == "" || len(out) == discoverMaxModels {
			continue
		}
		out = append(out, DiscoveredModel{
			Name:                name,
			Label:               m.DisplayName,
			ContextWindowTokens: m.InputTokenLimit,
			MaxOutputTokens:     m.OutputTokenLimit,
		})
	}
	return out, nil
}

// azureDeployments reads the data-plane deployments list: what an Azure
// OpenAI door serves is its DEPLOYMENTS (each an alias of an underlying
// model), so the deployment id is the model NAME and the underlying model its
// label.
func (d *HTTPDiscoverer) azureDeployments(ctx context.Context, endpoint, apiVersion, apiKey string) ([]DiscoveredModel, error) {
	base := strings.TrimSuffix(endpoint, "/")
	listURL := base + "/openai/deployments?api-version=" + url.QueryEscape(apiVersion)
	var doc struct {
		Data []struct {
			ID    string `json:"id"`
			Model string `json:"model"`
		} `json:"data"`
	}
	if err := d.getJSON(ctx, listURL, []header{{"api-key", apiKey}}, &doc); err != nil {
		return nil, err
	}
	out := make([]DiscoveredModel, 0, len(doc.Data))
	for _, m := range doc.Data {
		if m.ID == "" || len(out) == discoverMaxModels {
			continue
		}
		out = append(out, DiscoveredModel{Name: m.ID, Label: m.Model})
	}
	return out, nil
}
