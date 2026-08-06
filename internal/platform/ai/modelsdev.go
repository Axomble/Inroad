package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// ModelsDevURL is the community model registry the catalog source reads.
// There is NO shipped catalog file: native-provider model metadata comes from
// this document at runtime, cached in Postgres with serve-stale-on-failure.
const ModelsDevURL = "https://models.dev/api.json"

const (
	// catalogTTL is how long a cached snapshot is considered fresh.
	catalogTTL = 24 * time.Hour
	// catalogFetchTimeout bounds the models.dev round trip (spec §3).
	catalogFetchTimeout = 15 * time.Second
	// catalogMaxBytes caps the payload read — the document is ~1–2 MB today;
	// a response beyond this is treated as a fetch failure, never buffered.
	catalogMaxBytes = 16 << 20
)

// ErrCatalogUnavailable is returned when there is no cached snapshot AND the
// fetch failed — the only case that is an error. Callers treat it as "native
// model lists are empty" (discovery and manual entry still work), per the
// serve-stale-on-failure posture.
var ErrCatalogUnavailable = errors.New("ai: models.dev catalog unavailable")

// NativeModel is one models.dev entry mapped into our shape. Name is the
// model id the provider SDK receives; costs are USD per million tokens.
type NativeModel struct {
	Name                string
	Label               string
	ContextWindowTokens int
	MaxOutputTokens     int
	SupportsReasoning   bool
	InputCostPerMTok    float64
	OutputCostPerMTok   float64
}

// nativeCatalogKind maps a provider KIND to its models.dev provider key.
// Kinds absent here (gateways, clouds) have no native list — their models
// come from discovery or manual entry.
var nativeCatalogKind = map[string]string{
	KindAnthropic: "anthropic",
	KindOpenAI:    "openai",
	KindGoogle:    "google",
}

// NativeCatalogKey returns the models.dev provider key for a kind, if it has
// one.
func NativeCatalogKey(kind string) (string, bool) {
	k, ok := nativeCatalogKind[kind]
	return k, ok
}

// CacheStore persists the deployment-global models.dev snapshot. Satisfied by
// PgCatalogCache; a fake in tests.
type CacheStore interface {
	// Get returns the cached payload and when it was fetched. It MUST return
	// ErrCacheMiss (wrapped is fine) when no snapshot exists.
	Get(ctx context.Context) (payload []byte, fetchedAt time.Time, err error)
	Put(ctx context.Context, payload []byte) error
}

// ErrCacheMiss is the sentinel a CacheStore returns when no snapshot exists,
// distinguishing first-run (fetch required) from a real backend error.
var ErrCacheMiss = errors.New("ai: catalog cache miss")

// CatalogSource serves native-provider model metadata from models.dev with a
// 24h Postgres cache and serve-stale-on-failure: a fetch failure with ANY
// cached copy serves the stale copy; only no-cache-and-no-fetch errors.
type CatalogSource struct {
	store  CacheStore
	client *http.Client
	url    string
	now    func() time.Time
}

// NewCatalogSource builds the source over a cache store. url "" selects the
// real registry; tests point it at a fake server.
func NewCatalogSource(store CacheStore, url string) *CatalogSource {
	if url == "" {
		url = ModelsDevURL
	}
	return &CatalogSource{
		store:  store,
		client: &http.Client{Timeout: catalogFetchTimeout},
		url:    url,
		now:    time.Now,
	}
}

// NativeModels returns the models.dev models for one provider key
// ("anthropic", "openai", "google"), refreshing the cache when stale. Entries
// without positive context/output limits are skipped — the UI and runtime
// both need real windows.
func (s *CatalogSource) NativeModels(ctx context.Context, providerKey string) ([]NativeModel, error) {
	payload, err := s.payload(ctx)
	if err != nil {
		return nil, err
	}
	return parseModelsDev(payload, providerKey)
}

// payload returns a fresh-enough snapshot: cached-and-fresh wins, then a
// refetch, then the stale copy, and only then ErrCatalogUnavailable. A real
// cache-backend error is surfaced as-is (never masked as "unavailable").
func (s *CatalogSource) payload(ctx context.Context) ([]byte, error) {
	cached, fetchedAt, err := s.store.Get(ctx)
	haveCache := err == nil
	if err != nil && !errors.Is(err, ErrCacheMiss) {
		return nil, fmt.Errorf("ai: read catalog cache: %w", err)
	}
	if haveCache && s.now().Sub(fetchedAt) < catalogTTL {
		return cached, nil
	}

	fresh, fetchErr := s.fetch(ctx)
	if fetchErr == nil {
		// A write failure must not discard a good fetch: serve it and surface
		// nothing — the next call retries the write.
		_ = s.store.Put(ctx, fresh)
		return fresh, nil
	}
	if haveCache {
		return cached, nil // serve-stale-on-failure
	}
	return nil, fmt.Errorf("%w: %w", ErrCatalogUnavailable, fetchErr)
}

func (s *CatalogSource) fetch(ctx context.Context) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, http.NoBody)
	if err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("models.dev returned %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, catalogMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > catalogMaxBytes {
		return nil, fmt.Errorf("models.dev payload exceeds %d bytes", catalogMaxBytes)
	}
	// Validate it parses before caching — a truncated or HTML error body must
	// not poison the last-known-good snapshot.
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(body, &probe); err != nil {
		return nil, fmt.Errorf("models.dev payload is not valid JSON: %w", err)
	}
	return body, nil
}

// modelsdevDoc mirrors the slice of https://models.dev/api.json we read:
// top-level map providerKey → {models: map modelKey → entry}.
type modelsdevEntry struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Reasoning bool   `json:"reasoning"`
	Cost      struct {
		Input  float64 `json:"input"`
		Output float64 `json:"output"`
	} `json:"cost"`
	Limit struct {
		Context int `json:"context"`
		Output  int `json:"output"`
	} `json:"limit"`
}

func parseModelsDev(payload []byte, providerKey string) ([]NativeModel, error) {
	var doc map[string]struct {
		Models map[string]modelsdevEntry `json:"models"`
	}
	if err := json.Unmarshal(payload, &doc); err != nil {
		return nil, fmt.Errorf("ai: parse models.dev payload: %w", err)
	}
	provider, ok := doc[providerKey]
	if !ok {
		return nil, nil
	}
	out := make([]NativeModel, 0, len(provider.Models))
	for key, m := range provider.Models {
		name := m.ID
		if name == "" {
			name = key
		}
		label := m.Name
		if label == "" {
			label = name
		}
		if m.Limit.Context <= 0 || m.Limit.Output <= 0 {
			continue // unusable without real windows
		}
		out = append(out, NativeModel{
			Name:                name,
			Label:               label,
			ContextWindowTokens: m.Limit.Context,
			MaxOutputTokens:     m.Limit.Output,
			SupportsReasoning:   m.Reasoning,
			InputCostPerMTok:    m.Cost.Input,
			OutputCostPerMTok:   m.Cost.Output,
		})
	}
	// Order by name so the merged model list is stable across requests (map
	// iteration order is random).
	slices.SortFunc(out, func(a, b NativeModel) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// PgCatalogCache is the sqlc-backed CacheStore over ai_catalog_cache.
type PgCatalogCache struct {
	q *gen.Queries
}

func NewPgCatalogCache(q *gen.Queries) *PgCatalogCache { return &PgCatalogCache{q: q} }

func (c *PgCatalogCache) Get(ctx context.Context) ([]byte, time.Time, error) {
	row, err := c.q.GetAICatalogCache(ctx)
	if err != nil {
		// pgx.ErrNoRows → cache miss; anything else is a real backend error.
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, time.Time{}, ErrCacheMiss
		}
		return nil, time.Time{}, err
	}
	return row.Payload, row.FetchedAt.Time, nil
}

func (c *PgCatalogCache) Put(ctx context.Context, payload []byte) error {
	return c.q.UpsertAICatalogCache(ctx, payload)
}
