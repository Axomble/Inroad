package apikey

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// fakeStore is an in-memory Store for unit tests — no database. It records
// Create params so a test can assert exactly what was persisted (crucially: the
// secret hash, never a raw secret).
type fakeStore struct {
	created     []CreateParams
	byPrefix    map[string]gen.ApiKey
	list        []gen.ListApiKeysByWorkspaceRow
	revokeRows  int64
	revoked     [][2]uuid.UUID // {ws, id}
	touched     []uuid.UUID
	touchSignal chan uuid.UUID // when set, TouchLastUsed sends here instead of appending
	createErr   error
}

func newFakeStore() *fakeStore {
	return &fakeStore{byPrefix: map[string]gen.ApiKey{}}
}

func (f *fakeStore) Create(_ context.Context, p CreateParams) (gen.ApiKey, error) {
	if f.createErr != nil {
		return gen.ApiKey{}, f.createErr
	}
	f.created = append(f.created, p)
	k := gen.ApiKey{
		ID:              uuid.New(),
		WorkspaceID:     p.WorkspaceID,
		CreatedByUserID: pgtype.UUID{Bytes: p.CreatedBy, Valid: true},
		Name:            p.Name,
		Prefix:          p.Prefix,
		SecretHash:      p.SecretHash,
		Scopes:          p.Scopes,
		IpAllowlist:     p.IPAllowlist,
		RateLimitPerMin: p.RateLimitPerMin,
		CreatedAt:       pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}
	if p.ExpiresAt != nil {
		k.ExpiresAt = pgtype.Timestamptz{Time: *p.ExpiresAt, Valid: true}
	}
	f.byPrefix[p.Prefix] = k
	return k, nil
}

func (f *fakeStore) GetByPrefix(_ context.Context, prefix string) (gen.ApiKey, error) {
	k, ok := f.byPrefix[prefix]
	if !ok {
		return gen.ApiKey{}, pgx.ErrNoRows
	}
	return k, nil
}

func (f *fakeStore) ListByWorkspace(_ context.Context, _ uuid.UUID) ([]gen.ListApiKeysByWorkspaceRow, error) {
	return f.list, nil
}

func (f *fakeStore) Revoke(_ context.Context, ws, id uuid.UUID) (int64, error) {
	f.revoked = append(f.revoked, [2]uuid.UUID{ws, id})
	return f.revokeRows, nil
}

func (f *fakeStore) TouchLastUsed(_ context.Context, id uuid.UUID) error {
	if f.touchSignal != nil {
		f.touchSignal <- id
		return nil
	}
	f.touched = append(f.touched, id)
	return nil
}

func newTestService(store Store) *Service {
	s := NewService(store)
	s.now = func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }
	return s
}

// TestCreateReturnsTokenOnceAndPersistsOnlyHash proves Create hands back the full
// token exactly once and stores ONLY its SHA-256 — the raw secret never lands in
// the persisted params.
func TestCreateReturnsTokenOnceAndPersistsOnlyHash(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)

	view, token, err := svc.Create(context.Background(), CreateInput{
		WorkspaceID: uuid.New(),
		CreatedBy:   uuid.New(),
		Name:        "ci",
		Scopes:      []string{auth.ScopeContactsRead, auth.ScopeContactsWrite},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !strings.HasPrefix(token, tokenScheme) {
		t.Fatalf("token %q missing scheme", token)
	}
	if len(store.created) != 1 {
		t.Fatalf("persisted %d rows, want 1", len(store.created))
	}
	got := store.created[0]

	// The persisted hash must equal SHA-256 of the token's secret part, and the raw
	// secret must appear nowhere in the persisted params.
	_, secret, _ := strings.Cut(strings.TrimPrefix(token, tokenScheme), "_")
	want := sha256.Sum256([]byte(secret))
	if string(got.SecretHash) != string(want[:]) {
		t.Fatal("persisted hash is not SHA-256 of the returned secret")
	}
	if strings.Contains(string(got.SecretHash), secret) {
		t.Fatal("raw secret leaked into the persisted hash")
	}
	if view.Prefix != got.Prefix || !strings.Contains(token, view.Prefix) {
		t.Fatalf("view.Prefix %q inconsistent with token %q", view.Prefix, token)
	}
	// Scopes de-duplicated and preserved.
	if len(got.Scopes) != 2 {
		t.Fatalf("scopes = %v, want 2", got.Scopes)
	}
}

// TestCreateRejectsUnknownAndEmptyScopes proves scope validation at the boundary.
func TestCreateRejectsUnknownAndEmptyScopes(t *testing.T) {
	svc := newTestService(newFakeStore())
	base := CreateInput{WorkspaceID: uuid.New(), CreatedBy: uuid.New(), Name: "k"}

	base.Scopes = nil
	if _, _, err := svc.Create(context.Background(), base); !errors.Is(err, ErrInvalidScope) {
		t.Fatalf("empty scopes: err = %v, want ErrInvalidScope", err)
	}
	base.Scopes = []string{"mailboxes:read", "not-a-real-scope"}
	if _, _, err := svc.Create(context.Background(), base); !errors.Is(err, ErrInvalidScope) {
		t.Fatalf("unknown scope: err = %v, want ErrInvalidScope", err)
	}
}

// TestCreateValidatesAllowlistExpiryAndRate covers the remaining boundary checks.
func TestCreateValidatesAllowlistExpiryAndRate(t *testing.T) {
	svc := newTestService(newFakeStore())
	ok := CreateInput{WorkspaceID: uuid.New(), CreatedBy: uuid.New(), Name: "k", Scopes: []string{auth.ScopeListsRead}}

	bad := ok
	bad.IPAllowlist = []string{"not-an-ip"}
	if _, _, err := svc.Create(context.Background(), bad); !errors.Is(err, ErrInvalidIP) {
		t.Fatalf("bad ip: err = %v, want ErrInvalidIP", err)
	}

	past := svc.now().Add(-time.Hour)
	bad = ok
	bad.ExpiresAt = &past
	if _, _, err := svc.Create(context.Background(), bad); !errors.Is(err, ErrInvalidExpiry) {
		t.Fatalf("past expiry: err = %v, want ErrInvalidExpiry", err)
	}

	zero := 0
	bad = ok
	bad.RateLimitPerMin = &zero
	if _, _, err := svc.Create(context.Background(), bad); !errors.Is(err, ErrInvalidRateLimit) {
		t.Fatalf("zero rate: err = %v, want ErrInvalidRateLimit", err)
	}
}

// TestCreateCanonicalizesAllowlist proves a bare IP becomes a host route and a
// CIDR is masked to canonical form before persisting.
func TestCreateCanonicalizesAllowlist(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)
	_, _, err := svc.Create(context.Background(), CreateInput{
		WorkspaceID: uuid.New(), CreatedBy: uuid.New(), Name: "k",
		Scopes:      []string{auth.ScopeMailboxesRead},
		IPAllowlist: []string{"203.0.113.7", "10.1.2.3/16"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got := store.created[0].IPAllowlist
	if len(got) != 2 || got[0] != "203.0.113.7/32" || got[1] != "10.1.0.0/16" {
		t.Fatalf("allowlist = %v, want [203.0.113.7/32 10.1.0.0/16]", got)
	}
}

// TestRevokeIdempotentAndNotFound proves a 1-row revoke succeeds (idempotent at
// the SQL layer) and a 0-row revoke maps to ErrNotFound (unknown / cross-tenant).
func TestRevokeIdempotentAndNotFound(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)
	ws, id := uuid.New(), uuid.New()

	store.revokeRows = 1
	if err := svc.Revoke(context.Background(), ws, id); err != nil {
		t.Fatalf("revoke (1 row): %v", err)
	}
	store.revokeRows = 0
	if err := svc.Revoke(context.Background(), ws, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoke (0 rows): err = %v, want ErrNotFound", err)
	}
	if store.revoked[0] != [2]uuid.UUID{ws, id} {
		t.Fatal("revoke was not workspace-pinned with (ws, id)")
	}
}

// TestListReturnsViewsWithoutSecret proves List projects rows to views and — by
// construction, since KeyView has no secret/hash field — cannot carry one.
func TestListReturnsViewsWithoutSecret(t *testing.T) {
	store := newFakeStore()
	store.list = []gen.ListApiKeysByWorkspaceRow{{
		ID: uuid.New(), Name: "k", Prefix: "abcdefgh", Scopes: []string{auth.ScopeListsRead},
		CreatedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}}
	svc := newTestService(store)
	views, err := svc.List(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(views) != 1 || views[0].Prefix != "abcdefgh" {
		t.Fatalf("views = %+v", views)
	}
}
