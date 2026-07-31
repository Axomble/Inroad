package apikey

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

var (
	// ErrInvalidScope is returned when a requested scope is outside the owned
	// vocabulary (auth.AllScopes) or the scope set is empty (a key that authorizes
	// nothing is a configuration error, not a valid key).
	ErrInvalidScope = errors.New("unknown or empty scope")
	// ErrInvalidIP is returned when an ip_allowlist entry is not a valid IP or CIDR.
	ErrInvalidIP = errors.New("invalid ip allowlist entry")
	// ErrInvalidExpiry is returned when expires_at is in the past.
	ErrInvalidExpiry = errors.New("expiry must be in the future")
	// ErrInvalidRateLimit is returned when rate_limit_per_min is not positive.
	ErrInvalidRateLimit = errors.New("rate limit must be positive")
	// ErrNotFound is returned when revoking a key that does not exist in the
	// caller's workspace (unknown id or cross-tenant).
	ErrNotFound = errors.New("api key not found")
)

// prefixRetries bounds how many times Create regenerates a colliding public
// prefix. A collision on a 40-bit random prefix is astronomically unlikely; the
// retry is defensive hygiene, not a hot path.
const prefixRetries = 5

// Service implements the apikey business rules over a Store. It holds no secrets
// itself: the raw secret is generated per-create, returned once, and only its
// hash is persisted.
type Service struct {
	store Store
	// now is overridable in tests for deterministic expiry validation.
	now func() time.Time
}

// NewService builds a Service over store.
func NewService(store Store) *Service {
	return &Service{store: store, now: time.Now}
}

// CreateInput is the validated request to mint a key (options struct: the field
// count is past the ~3 threshold and several are optional).
type CreateInput struct {
	WorkspaceID     uuid.UUID
	CreatedBy       uuid.UUID
	Name            string
	Scopes          []string
	IPAllowlist     []string   // IPs or CIDRs; nil/empty = no restriction
	RateLimitPerMin *int       // nil = unlimited
	ExpiresAt       *time.Time // nil = never
}

// KeyView is the non-secret projection of a key returned by Create (as metadata)
// and List. It carries NO secret hash by construction.
type KeyView struct {
	ID              uuid.UUID
	Name            string
	Prefix          string
	Scopes          []string
	IPAllowlist     []string
	RateLimitPerMin *int32
	ExpiresAt       *time.Time
	RevokedAt       *time.Time
	LastUsedAt      *time.Time
	CreatedBy       *uuid.UUID
	CreatedAt       time.Time
}

// Create validates the request, mints a fresh token, persists only its hash, and
// returns the key metadata plus the FULL token exactly once (it is never
// retrievable again). The raw secret is never logged and never stored.
func (s *Service) Create(ctx context.Context, in CreateInput) (KeyView, string, error) {
	scopes, err := normalizeScopes(in.Scopes)
	if err != nil {
		return KeyView{}, "", err
	}
	allowlist, err := normalizeAllowlist(in.IPAllowlist)
	if err != nil {
		return KeyView{}, "", err
	}
	if in.ExpiresAt != nil && !in.ExpiresAt.After(s.now()) {
		return KeyView{}, "", ErrInvalidExpiry
	}
	var rate *int32
	if in.RateLimitPerMin != nil {
		if *in.RateLimitPerMin <= 0 {
			return KeyView{}, "", ErrInvalidRateLimit
		}
		v := int32(*in.RateLimitPerMin)
		rate = &v
	}

	for attempt := 0; attempt < prefixRetries; attempt++ {
		prefix, token, secretHash, err := newToken()
		if err != nil {
			return KeyView{}, "", err
		}
		key, err := s.store.Create(ctx, CreateParams{
			WorkspaceID:     in.WorkspaceID,
			CreatedBy:       in.CreatedBy,
			Name:            in.Name,
			Prefix:          prefix,
			SecretHash:      secretHash,
			Scopes:          scopes,
			IPAllowlist:     allowlist,
			RateLimitPerMin: rate,
			ExpiresAt:       in.ExpiresAt,
		})
		if err != nil {
			if isUniquePrefixViolation(err) {
				continue // regenerate a fresh prefix and retry
			}
			return KeyView{}, "", err
		}
		return viewFromKey(key), token, nil
	}
	return KeyView{}, "", fmt.Errorf("apikey: could not allocate a unique prefix after %d attempts", prefixRetries)
}

// List returns the workspace's keys as non-secret views, newest first.
func (s *Service) List(ctx context.Context, ws uuid.UUID) ([]KeyView, error) {
	rows, err := s.store.ListByWorkspace(ctx, ws)
	if err != nil {
		return nil, err
	}
	out := make([]KeyView, 0, len(rows))
	for _, r := range rows {
		out = append(out, viewFromRow(r))
	}
	return out, nil
}

// Revoke revokes (ws, id). It is idempotent (re-revoking succeeds) and
// tenant-pinned; a key absent from ws yields ErrNotFound so a caller cannot probe
// or revoke another workspace's key.
func (s *Service) Revoke(ctx context.Context, ws, id uuid.UUID) error {
	n, err := s.store.Revoke(ctx, ws, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// normalizeScopes validates that every requested scope is part of the owned
// vocabulary and returns them de-duplicated. An empty set is rejected.
func normalizeScopes(scopes []string) ([]string, error) {
	if len(scopes) == 0 {
		return nil, ErrInvalidScope
	}
	seen := make(map[string]struct{}, len(scopes))
	out := make([]string, 0, len(scopes))
	for _, sc := range scopes {
		if !auth.IsKnownScope(sc) {
			return nil, fmt.Errorf("%w: %q", ErrInvalidScope, sc)
		}
		if _, dup := seen[sc]; dup {
			continue
		}
		seen[sc] = struct{}{}
		out = append(out, sc)
	}
	return out, nil
}

// normalizeAllowlist parses each entry as an IP or CIDR and returns the canonical
// CIDR string form (a bare IP becomes a /32 or /128 host route). A nil/empty input
// means "no restriction" and is stored as NULL.
func normalizeAllowlist(entries []string) ([]string, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		p, err := parseCIDR(e)
		if err != nil {
			return nil, fmt.Errorf("%w: %q", ErrInvalidIP, e)
		}
		out = append(out, p.String())
	}
	return out, nil
}

// parseCIDR accepts either a CIDR ("10.0.0.0/8") or a bare IP ("203.0.113.4",
// promoted to a host route), returning the canonical masked prefix.
func parseCIDR(s string) (netip.Prefix, error) {
	if p, err := netip.ParsePrefix(s); err == nil {
		return p.Masked(), nil
	}
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	return netip.PrefixFrom(addr, addr.BitLen()), nil
}

// isUniquePrefixViolation reports whether err is a Postgres unique-constraint
// violation (SQLSTATE 23505) — the collision Create retries on.
func isUniquePrefixViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func viewFromKey(k gen.ApiKey) KeyView {
	return KeyView{
		ID:              k.ID,
		Name:            k.Name,
		Prefix:          k.Prefix,
		Scopes:          k.Scopes,
		IPAllowlist:     k.IpAllowlist,
		RateLimitPerMin: k.RateLimitPerMin,
		ExpiresAt:       timePtr(k.ExpiresAt),
		RevokedAt:       timePtr(k.RevokedAt),
		LastUsedAt:      timePtr(k.LastUsedAt),
		CreatedBy:       uuidPtr(k.CreatedByUserID),
		CreatedAt:       k.CreatedAt.Time,
	}
}

func viewFromRow(r gen.ListApiKeysByWorkspaceRow) KeyView {
	return KeyView{
		ID:              r.ID,
		Name:            r.Name,
		Prefix:          r.Prefix,
		Scopes:          r.Scopes,
		IPAllowlist:     r.IpAllowlist,
		RateLimitPerMin: r.RateLimitPerMin,
		ExpiresAt:       timePtr(r.ExpiresAt),
		RevokedAt:       timePtr(r.RevokedAt),
		LastUsedAt:      timePtr(r.LastUsedAt),
		CreatedBy:       uuidPtr(r.CreatedByUserID),
		CreatedAt:       r.CreatedAt.Time,
	}
}
