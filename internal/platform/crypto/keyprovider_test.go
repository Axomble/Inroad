package crypto

import (
	"bytes"
	"context"
	"encoding/base64"
	"testing"
)

func dek32() []byte { return bytes.Repeat([]byte{0x22}, 32) }

func TestLocalKeyProviderWrapUnwrapRoundTrip(t *testing.T) {
	p, err := NewLocalKeyProvider(key32())
	if err != nil {
		t.Fatalf("NewLocalKeyProvider: %v", err)
	}
	dek := dek32()
	wrapped, err := p.Wrap(context.Background(), dek, nil)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if bytes.Contains(wrapped, dek) {
		t.Fatal("wrapped blob leaked plaintext DEK")
	}
	got, err := p.Unwrap(context.Background(), wrapped, nil)
	if err != nil {
		t.Fatalf("Unwrap: %v", err)
	}
	if !bytes.Equal(got, dek) {
		t.Fatalf("round-trip mismatch: got %x", got)
	}
}

func TestLocalKeyProviderUnwrapRejectsTamperedBlob(t *testing.T) {
	p, err := NewLocalKeyProvider(key32())
	if err != nil {
		t.Fatalf("NewLocalKeyProvider: %v", err)
	}
	wrapped, err := p.Wrap(context.Background(), dek32(), nil)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	wrapped[len(wrapped)-1] ^= 0xff
	if _, err := p.Unwrap(context.Background(), wrapped, nil); err == nil {
		t.Fatal("expected Unwrap to fail on tampered blob")
	}
}

func TestLocalKeyProviderName(t *testing.T) {
	p, err := NewLocalKeyProvider(key32())
	if err != nil {
		t.Fatalf("NewLocalKeyProvider: %v", err)
	}
	if got := p.Name(); got != "local" {
		t.Fatalf("Name() = %q, want %q", got, "local")
	}
}

func TestNewLocalKeyProviderRejectsBadKey(t *testing.T) {
	if _, err := NewLocalKeyProvider([]byte("short")); err == nil {
		t.Fatal("expected error for non-32-byte key")
	}
}

// Wrapping the same DEK twice must yield different wrapped bytes (fresh nonce
// each wrap), yet both must unwrap to the original — proving nonce randomness.
func TestLocalKeyProviderFreshNoncePerWrap(t *testing.T) {
	p, err := NewLocalKeyProvider(key32())
	if err != nil {
		t.Fatalf("NewLocalKeyProvider: %v", err)
	}
	dek := dek32()
	w1, err := p.Wrap(context.Background(), dek, nil)
	if err != nil {
		t.Fatalf("Wrap #1: %v", err)
	}
	w2, err := p.Wrap(context.Background(), dek, nil)
	if err != nil {
		t.Fatalf("Wrap #2: %v", err)
	}
	if bytes.Equal(w1, w2) {
		t.Fatal("two wraps of the same DEK produced identical bytes (nonce reuse)")
	}
	u1, err := p.Unwrap(context.Background(), w1, nil)
	if err != nil {
		t.Fatalf("Unwrap w1: %v", err)
	}
	u2, err := p.Unwrap(context.Background(), w2, nil)
	if err != nil {
		t.Fatalf("Unwrap w2: %v", err)
	}
	if !bytes.Equal(u1, dek) || !bytes.Equal(u2, dek) {
		t.Fatal("wraps did not both unwrap to the original DEK")
	}
}

// The KEK is an HKDF subkey of the master key, domain-separated from the raw
// master key that seals legacy v1 field blobs. A blob wrapped by the KEK must
// therefore NOT decrypt under a plain master-key Sealer.
func TestLocalKeyProviderKEKDomainSeparation(t *testing.T) {
	master := key32()
	p, err := NewLocalKeyProvider(master)
	if err != nil {
		t.Fatalf("NewLocalKeyProvider: %v", err)
	}
	wrapped, err := p.Wrap(context.Background(), dek32(), nil)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	// The legacy Sealer keys AES-GCM with the raw master key; it must not be
	// able to open a KEK-wrapped blob (different key domain). The Sealer wire
	// format matches Wrap's (nonce||ct), so base64 is the only re-encoding.
	legacy, err := NewSealer(master)
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}
	if _, err := legacy.Open(base64.StdEncoding.EncodeToString(wrapped)); err == nil {
		t.Fatal("KEK-wrapped blob opened under the raw master key — no domain separation")
	}
}

// aad binds the wrapped DEK to a context (e.g. workspace id): the same aad must
// be supplied on Unwrap, and nil aad is a distinct (optional) domain.
func TestLocalKeyProviderAADBinding(t *testing.T) {
	p, err := NewLocalKeyProvider(key32())
	if err != nil {
		t.Fatalf("NewLocalKeyProvider: %v", err)
	}
	dek := dek32()
	aadA := []byte("ws:aaaaaaaa")
	aadB := []byte("ws:bbbbbbbb")

	wrapped, err := p.Wrap(context.Background(), dek, aadA)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	// Correct aad round-trips.
	got, err := p.Unwrap(context.Background(), wrapped, aadA)
	if err != nil {
		t.Fatalf("Unwrap with matching aad: %v", err)
	}
	if !bytes.Equal(got, dek) {
		t.Fatalf("aad round-trip mismatch: got %x", got)
	}
	// A different aad must fail GCM authentication.
	if _, err := p.Unwrap(context.Background(), wrapped, aadB); err == nil {
		t.Fatal("expected Unwrap to fail with a mismatched aad")
	}
	// nil aad against an aad-bound blob must also fail.
	if _, err := p.Unwrap(context.Background(), wrapped, nil); err == nil {
		t.Fatal("expected Unwrap to fail with nil aad against an aad-bound blob")
	}
}
