package identity

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
)

// fakeUserToken is the fake store's in-memory row for a single-use user
// token (email verify, password reset, ...), mirroring the shape of the
// real user_tokens table closely enough to exercise the storeIface contract.
type fakeUserToken struct {
	userID    uuid.UUID
	kind      string
	issuedAt  time.Time
	expiresAt time.Time
	consumed  bool
}

// IssueUserToken mints a raw opaque token and records it keyed by the SHA-256
// hash of the raw value, matching the real Store's CreateUserToken semantics.
func (f *fakeStore) IssueUserToken(ctx context.Context, userID uuid.UUID, kind string, ttl time.Duration) (string, error) {
	raw, hash, err := auth.NewOpaqueToken()
	if err != nil {
		return "", err
	}
	now := time.Now()
	f.tokens[hashKey(hash)] = &fakeUserToken{
		userID:    userID,
		kind:      kind,
		issuedAt:  now,
		expiresAt: now.Add(ttl),
	}
	return raw, nil
}

// ConsumeUserToken looks a token up by hash and marks it consumed, mirroring
// the real Store's single-use ConsumeUserToken query: a miss, kind mismatch,
// already-consumed, or expired token all yield ErrTokenInvalid.
func (f *fakeStore) ConsumeUserToken(ctx context.Context, raw, kind string) (uuid.UUID, error) {
	tok, ok := f.tokens[hashKey(auth.HashToken(raw))]
	if !ok || tok.kind != kind || tok.consumed || time.Now().After(tok.expiresAt) {
		return uuid.Nil, ErrTokenInvalid
	}
	tok.consumed = true
	return tok.userID, nil
}

// CountRecentUserTokens counts tokens of kind issued to userID since the
// given time, mirroring the real Store's rate-limit support query.
func (f *fakeStore) CountRecentUserTokens(ctx context.Context, userID uuid.UUID, kind string, since time.Time) (int64, error) {
	var n int64
	for _, tok := range f.tokens {
		if tok.userID == userID && tok.kind == kind && tok.issuedAt.After(since) {
			n++
		}
	}
	return n, nil
}

func TestIssueUserTokenHashesRawForStorage(t *testing.T) {
	store := newFakeStore()
	userID := uuid.New()

	raw, err := store.IssueUserToken(context.Background(), userID, "email_verify", time.Hour)
	if err != nil {
		t.Fatalf("IssueUserToken: %v", err)
	}
	if raw == "" {
		t.Fatal("expected non-empty raw token")
	}
	if _, ok := store.tokens[hashKey(auth.HashToken(raw))]; !ok {
		t.Fatal("expected token to be stored keyed by SHA-256 hash of raw")
	}
}

func TestConsumeUserTokenValidReturnsUserID(t *testing.T) {
	store := newFakeStore()
	userID := uuid.New()

	raw, err := store.IssueUserToken(context.Background(), userID, "email_verify", time.Hour)
	if err != nil {
		t.Fatalf("IssueUserToken: %v", err)
	}
	got, err := store.ConsumeUserToken(context.Background(), raw, "email_verify")
	if err != nil {
		t.Fatalf("ConsumeUserToken: %v", err)
	}
	if got != userID {
		t.Fatalf("expected user id %s, got %s", userID, got)
	}
}

func TestConsumeUserTokenWrongRawIsInvalid(t *testing.T) {
	store := newFakeStore()
	if _, err := store.IssueUserToken(context.Background(), uuid.New(), "email_verify", time.Hour); err != nil {
		t.Fatalf("IssueUserToken: %v", err)
	}
	_, err := store.ConsumeUserToken(context.Background(), "not-the-real-token", "email_verify")
	if !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("expected ErrTokenInvalid, got %v", err)
	}
}

func TestConsumeUserTokenWrongKindIsInvalid(t *testing.T) {
	store := newFakeStore()
	raw, err := store.IssueUserToken(context.Background(), uuid.New(), "email_verify", time.Hour)
	if err != nil {
		t.Fatalf("IssueUserToken: %v", err)
	}
	_, err = store.ConsumeUserToken(context.Background(), raw, "password_reset")
	if !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("expected ErrTokenInvalid on kind mismatch, got %v", err)
	}
}

func TestConsumeUserTokenAgainIsInvalid(t *testing.T) {
	store := newFakeStore()
	raw, err := store.IssueUserToken(context.Background(), uuid.New(), "email_verify", time.Hour)
	if err != nil {
		t.Fatalf("IssueUserToken: %v", err)
	}
	if _, err := store.ConsumeUserToken(context.Background(), raw, "email_verify"); err != nil {
		t.Fatalf("first consume: %v", err)
	}
	if _, err := store.ConsumeUserToken(context.Background(), raw, "email_verify"); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("expected ErrTokenInvalid on second consume, got %v", err)
	}
}

func TestConsumeUserTokenExpiredIsInvalid(t *testing.T) {
	store := newFakeStore()
	raw, err := store.IssueUserToken(context.Background(), uuid.New(), "email_verify", -time.Minute)
	if err != nil {
		t.Fatalf("IssueUserToken: %v", err)
	}
	if _, err := store.ConsumeUserToken(context.Background(), raw, "email_verify"); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("expected ErrTokenInvalid on expired token, got %v", err)
	}
}

func TestCountRecentUserTokens(t *testing.T) {
	store := newFakeStore()
	userID := uuid.New()
	since := time.Now().Add(-time.Hour)

	for i := 0; i < 3; i++ {
		if _, err := store.IssueUserToken(context.Background(), userID, "password_reset", time.Hour); err != nil {
			t.Fatalf("IssueUserToken: %v", err)
		}
	}
	// A different kind and a different user must not be counted.
	if _, err := store.IssueUserToken(context.Background(), userID, "email_verify", time.Hour); err != nil {
		t.Fatalf("IssueUserToken: %v", err)
	}
	if _, err := store.IssueUserToken(context.Background(), uuid.New(), "password_reset", time.Hour); err != nil {
		t.Fatalf("IssueUserToken: %v", err)
	}

	n, err := store.CountRecentUserTokens(context.Background(), userID, "password_reset", since)
	if err != nil {
		t.Fatalf("CountRecentUserTokens: %v", err)
	}
	if n != 3 {
		t.Fatalf("expected 3 recent password_reset tokens, got %d", n)
	}
}
