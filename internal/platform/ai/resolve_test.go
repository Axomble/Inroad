package ai

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

func entry(provider uuid.UUID, name string) CatalogModel {
	return CatalogModel{
		ID: ModelID(provider, name), ProviderID: provider, Name: name,
		Label: name, ContextWindowTokens: 100000, MaxOutputTokens: 8192,
		Source: SourceCatalog,
	}
}

// TestResolveExplicitID proves an explicit id resolves iff present in the
// available set.
func TestResolveExplicitID(t *testing.T) {
	p := uuid.New()
	available := []CatalogModel{entry(p, "claude-sonnet-5"), entry(p, "gpt-5.2")}

	m, err := ResolveModel(ModelID(p, "gpt-5.2"), SentinelSmartModel, SentinelFastModel, available)
	if err != nil || m.Name != "gpt-5.2" {
		t.Fatalf("explicit id: got %+v, err %v", m, err)
	}
	if _, err := ResolveModel(ModelID(uuid.New(), "gpt-5.2"), "", "", available); !errors.Is(err, ErrNoModel) {
		t.Fatalf("foreign provider row must be ErrNoModel, got %v", err)
	}
}

// TestResolveSentinelWalksNamePreference proves the sentinel picks by bare
// model name in preference order, regardless of which door serves it.
func TestResolveSentinelWalksNamePreference(t *testing.T) {
	p := uuid.New()
	available := []CatalogModel{entry(p, "gpt-5.2"), entry(p, "claude-sonnet-5"), entry(p, "claude-haiku-4-5")}

	m, err := ResolveModel(SentinelSmartModel, SentinelSmartModel, SentinelFastModel, available)
	if err != nil || m.Name != "claude-sonnet-5" {
		t.Fatalf("smart must prefer claude-sonnet-5: got %+v, err %v", m, err)
	}
	m, err = ResolveModel(SentinelFastModel, "", "", available)
	if err != nil || m.Name != "claude-haiku-4-5" {
		t.Fatalf("fast must prefer claude-haiku-4-5: got %+v, err %v", m, err)
	}
}

// TestResolveSentinelFallsBackToFirstAvailable proves a pure-gateway
// workspace (no preference-list names) still resolves a sentinel.
func TestResolveSentinelFallsBackToFirstAvailable(t *testing.T) {
	p := uuid.New()
	available := []CatalogModel{entry(p, "llama-3.3-70b"), entry(p, "mixtral-8x22b")}

	m, err := ResolveModel(SentinelSmartModel, "", "", available)
	if err != nil || m.Name != "llama-3.3-70b" {
		t.Fatalf("fallback must pick the first available: got %+v, err %v", m, err)
	}
	if _, err := ResolveModel(SentinelSmartModel, "", "", nil); !errors.Is(err, ErrNoModel) {
		t.Fatalf("empty set must be ErrNoModel, got %v", err)
	}
}

// TestResolveSentinelDefersToWorkspaceDefault proves the workspace's stored
// explicit default beats the preference walk — and fails rather than falling
// back when that explicit choice is gone (the workspace asked for it).
func TestResolveSentinelDefersToWorkspaceDefault(t *testing.T) {
	p := uuid.New()
	available := []CatalogModel{entry(p, "claude-sonnet-5"), entry(p, "claude-opus-5")}

	m, err := ResolveModel(SentinelSmartModel, ModelID(p, "claude-opus-5"), "", available)
	if err != nil || m.Name != "claude-opus-5" {
		t.Fatalf("workspace default must win: got %+v, err %v", m, err)
	}
	if _, err := ResolveModel(SentinelFastModel, "", ModelID(uuid.New(), "gone"), available); !errors.Is(err, ErrNoModel) {
		t.Fatalf("vanished explicit default must be ErrNoModel, got %v", err)
	}
}
