package ai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// fakeCache is an in-memory CacheStore with an injectable backend error.
type fakeCache struct {
	payload   []byte
	fetchedAt time.Time
	getErr    error
	puts      int
}

func (f *fakeCache) Get(context.Context) ([]byte, time.Time, error) {
	if f.getErr != nil {
		return nil, time.Time{}, f.getErr
	}
	if f.payload == nil {
		return nil, time.Time{}, ErrCacheMiss
	}
	return f.payload, f.fetchedAt, nil
}

func (f *fakeCache) Put(_ context.Context, payload []byte) error {
	f.puts++
	f.payload = payload
	f.fetchedAt = time.Now()
	return nil
}

const modelsdevDoc = `{
  "anthropic": {"models": {
    "claude-sonnet-5": {"id":"claude-sonnet-5","name":"Claude Sonnet 5","reasoning":true,
      "cost":{"input":3,"output":15},"limit":{"context":1000000,"output":128000}},
    "no-limits": {"id":"no-limits","name":"Broken entry"}
  }},
  "openai": {"models": {
    "gpt-5.2": {"id":"gpt-5.2","name":"GPT-5.2","reasoning":true,
      "cost":{"input":1.25,"output":10},"limit":{"context":400000,"output":128000}}
  }}
}`

// TestNativeModelsFetchesAndCaches proves a cache miss triggers one fetch,
// the payload is cached, and entries without usable limits are skipped.
func TestNativeModelsFetchesAndCaches(t *testing.T) {
	hits := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte(modelsdevDoc))
	}))
	defer ts.Close()

	cache := &fakeCache{}
	src := NewCatalogSource(cache, ts.URL)

	models, err := src.NativeModels(context.Background(), "anthropic")
	if err != nil {
		t.Fatalf("NativeModels: %v", err)
	}
	if len(models) != 1 || models[0].Name != "claude-sonnet-5" || !models[0].SupportsReasoning {
		t.Fatalf("anthropic models wrong (no-limits entry must be skipped): %+v", models)
	}
	if models[0].InputCostPerMTok != 3 || models[0].ContextWindowTokens != 1000000 {
		t.Fatalf("metadata mapping wrong: %+v", models[0])
	}
	if hits != 1 || cache.puts != 1 {
		t.Fatalf("want exactly one fetch+put, got hits=%d puts=%d", hits, cache.puts)
	}

	// A fresh cache serves without refetching.
	if _, err := src.NativeModels(context.Background(), "openai"); err != nil {
		t.Fatalf("second read: %v", err)
	}
	if hits != 1 {
		t.Fatalf("fresh cache must not refetch, got %d hits", hits)
	}

	// An unknown provider key is empty, not an error.
	none, err := src.NativeModels(context.Background(), "mistral")
	if err != nil || len(none) != 0 {
		t.Fatalf("unknown provider key must be empty: %+v, %v", none, err)
	}
}

// TestNativeModelsServesStaleOnFetchFailure proves the load-bearing posture:
// a stale snapshot + dead registry serves the stale copy; only
// no-cache-and-no-fetch is an error (ErrCatalogUnavailable).
func TestNativeModelsServesStaleOnFetchFailure(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusBadGateway)
	}))
	defer ts.Close()

	stale := &fakeCache{payload: []byte(modelsdevDoc), fetchedAt: time.Now().Add(-48 * time.Hour)}
	src := NewCatalogSource(stale, ts.URL)
	models, err := src.NativeModels(context.Background(), "openai")
	if err != nil || len(models) != 1 || models[0].Name != "gpt-5.2" {
		t.Fatalf("stale copy must be served on fetch failure: %+v, %v", models, err)
	}

	empty := &fakeCache{}
	src = NewCatalogSource(empty, ts.URL)
	if _, err := src.NativeModels(context.Background(), "openai"); !errors.Is(err, ErrCatalogUnavailable) {
		t.Fatalf("no cache + failed fetch must be ErrCatalogUnavailable, got %v", err)
	}
}

// TestNativeModelsRejectsGarbagePayload proves a non-JSON body (a proxy error
// page) is a fetch failure and cannot poison the cache.
func TestNativeModelsRejectsGarbagePayload(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>503</html>"))
	}))
	defer ts.Close()

	stale := &fakeCache{payload: []byte(modelsdevDoc), fetchedAt: time.Now().Add(-48 * time.Hour)}
	src := NewCatalogSource(stale, ts.URL)
	models, err := src.NativeModels(context.Background(), "anthropic")
	if err != nil || len(models) != 1 {
		t.Fatalf("garbage fetch must fall back to stale: %+v, %v", models, err)
	}
	if stale.puts != 0 {
		t.Fatalf("garbage payload must never be cached, puts=%d", stale.puts)
	}
}

// TestNativeModelsSurfacesRealCacheErrors proves a genuine cache-backend
// failure is surfaced, never masked as "unavailable" (which would silently
// hide a DB outage as an empty model list).
func TestNativeModelsSurfacesRealCacheErrors(t *testing.T) {
	boom := errors.New("db down")
	src := NewCatalogSource(&fakeCache{getErr: boom}, "http://127.0.0.1:0")
	if _, err := src.NativeModels(context.Background(), "anthropic"); !errors.Is(err, boom) {
		t.Fatalf("cache backend error must surface, got %v", err)
	}
}
