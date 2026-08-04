package ai

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"testing"
)

func validChatRequest() ChatRequest {
	return ChatRequest{Model: "model", MaxTokens: 128, Messages: []ChatMessage{{Role: RoleUser, Parts: []ChatPart{{Type: PartText, Text: "hello"}}}}}
}

func TestStreamingHTTPClientRejectsRedirects(t *testing.T) {
	c := (&streamerFactory{}).httpClient()
	if err := c.CheckRedirect(&http.Request{}, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatal(err)
	}
}

func TestValidateRequest(t *testing.T) {
	r := validChatRequest()
	if err := validateRequest(r); err != nil {
		t.Fatal(err)
	}
	r.MaxTokens = math.MaxInt32 + 1
	if err := validateRequest(r); !errors.Is(err, ErrBadProviderConfig) {
		t.Fatalf("error = %v", err)
	}
	r = validChatRequest()
	r.Messages[0].Role = "system"
	if err := validateRequest(r); !errors.Is(err, ErrBadProviderConfig) {
		t.Fatalf("error = %v", err)
	}
}

func TestToolValidation(t *testing.T) {
	r := validChatRequest()
	r.Tools = []ToolDef{{Name: "x", InputSchema: json.RawMessage(`{"type":"array"}`)}}
	if err := validateRequest(r); !errors.Is(err, ErrBadProviderConfig) {
		t.Fatalf("error = %v", err)
	}
	r.Tools = []ToolDef{{Name: "x"}, {Name: "x"}}
	if err := validateRequest(r); !errors.Is(err, ErrBadProviderConfig) {
		t.Fatalf("error = %v", err)
	}
}

func TestRequireHTTPBaseURL(t *testing.T) {
	for _, raw := range []string{"relative", "ftp://example.test", "https://user:pass@example.test", "https://example.test?q=1"} {
		if _, err := requireHTTPBaseURL(map[string]string{"base_url": raw}, KindOpenAICompatible, "base_url"); !errors.Is(err, ErrBadProviderConfig) {
			t.Errorf("%q error = %v", raw, err)
		}
	}
	got, err := requireHTTPBaseURL(map[string]string{"base_url": "http://localhost:11434/v1"}, KindOpenAICompatible, "base_url")
	if err != nil || got == "" {
		t.Fatalf("valid URL = %q, %v", got, err)
	}
}

func TestSharedStreamHelpers(t *testing.T) {
	got := normalizeUsage(10, 8, 3, 2, true)
	want := Usage{InputTokens: 10, OutputTokens: 5, ReasoningTokens: 3, CacheReadTokens: 2}
	if got != want {
		t.Fatalf("usage = %+v", got)
	}
	for input, want := range map[string]string{"": "{}", "{": "null", "[]": "null"} {
		if got := string(accumulatedToolInput(input)); got != want {
			t.Errorf("%q => %s", input, got)
		}
	}
	if err := providerError(KindOpenAI, context.Canceled); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
