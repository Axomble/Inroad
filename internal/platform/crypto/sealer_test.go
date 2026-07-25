package crypto

import (
	"bytes"
	"encoding/base64"
	"testing"
)

func key32() []byte { return bytes.Repeat([]byte{0x11}, 32) }

func bytesRepeat(b byte, n int) []byte { return bytes.Repeat([]byte{b}, n) }

func TestSealOpenRoundTrip(t *testing.T) {
	s, err := NewSealer(key32())
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}
	secret := []byte("smtp-app-password")
	token, err := s.Seal(secret)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if bytes.Contains([]byte(token), secret) {
		t.Fatal("ciphertext leaked plaintext")
	}
	got, err := s.Open(token)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(got, secret) {
		t.Fatalf("round-trip mismatch: got %q", got)
	}
}

func TestNewSealerRejectsBadKey(t *testing.T) {
	if _, err := NewSealer([]byte("short")); err == nil {
		t.Fatal("expected error for short key")
	}
}

func TestLegacySealerStaysV1(t *testing.T) {
	// The master-key legacy sealer must never write a version byte; its output
	// stays byte-compatible with pre-DEK ciphertext (base64(nonce||ct)).
	s, err := NewSealer(key32())
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}
	tok, err := s.Seal([]byte("v1-secret"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(tok)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	// nonce||ct length; and definitely no 0x02 envelope framing (the first byte
	// is a random nonce byte, not a version tag — assert length not the byte).
	if len(raw) != 12+len("v1-secret")+16 {
		t.Fatalf("unexpected v1 length %d", len(raw))
	}
}

func TestDEKSealerV2RoundTrip(t *testing.T) {
	dek := bytesRepeat(0x44, 32)
	aad := []byte("ws:11111111-1111-1111-1111-111111111111")
	s := newDEKSealer(dek, aad, nil)

	secret := []byte("oauth-refresh-token")
	tok, err := s.Seal(secret)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if bytes.Contains([]byte(tok), secret) {
		t.Fatal("ciphertext leaked plaintext")
	}
	raw, err := base64.StdEncoding.DecodeString(tok)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(raw) == 0 || raw[0] != 0x02 {
		t.Fatalf("expected v2 0x02 prefix, got %x", raw)
	}
	got, err := s.Open(tok)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(got, secret) {
		t.Fatalf("round-trip mismatch: %q", got)
	}
}

func TestDEKSealerRejectsAADMismatch(t *testing.T) {
	dek := bytesRepeat(0x44, 32)
	sa := newDEKSealer(dek, []byte("ws:aaaa"), nil)
	sb := newDEKSealer(dek, []byte("ws:bbbb"), nil) // same DEK, different AAD

	tok, err := sa.Seal([]byte("bound"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := sb.Open(tok); err == nil {
		t.Fatal("expected Open to fail under a different AAD")
	}
}

func TestDEKSealerOpensLegacyV1(t *testing.T) {
	master := key32()
	legacy, err := NewSealer(master)
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}
	v1tok, err := legacy.Seal([]byte("legacy"))
	if err != nil {
		t.Fatalf("legacy Seal: %v", err)
	}

	dek := bytesRepeat(0x55, 32)
	s := newDEKSealer(dek, []byte("ws:cccc"), legacy)

	got, err := s.Open(v1tok)
	if err != nil {
		t.Fatalf("Open legacy: %v", err)
	}
	if string(got) != "legacy" {
		t.Fatalf("legacy open mismatch: %q", got)
	}

	// Without a legacy fallback the same v1 blob is not openable by a DEK sealer.
	orphan := newDEKSealer(dek, []byte("ws:cccc"), nil)
	if _, err := orphan.Open(v1tok); err == nil {
		t.Fatal("expected v1 open to fail with no legacy sealer")
	}
}

// TestDEKSealerOpensLegacyV1WithLeading0x02 exercises the ambiguous fallback
// branch: a v1 blob whose first byte happens to be 0x02 will make the v2 parse
// attempt fail, and the DEK sealer must recover via its legacy master-key path.
func TestDEKSealerOpensLegacyV1WithLeading0x02(t *testing.T) {
	master := key32()
	legacy, err := NewSealer(master)
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}

	// Seal repeatedly until we land a v1 blob whose base64-decoded first byte
	// (the leading nonce byte) is 0x02 — the collision the fallback guards.
	var v1tok string
	const secret = "collides"
	for i := 0; i < 100000; i++ {
		tok, serr := legacy.Seal([]byte(secret))
		if serr != nil {
			t.Fatalf("Seal: %v", serr)
		}
		raw, derr := base64.StdEncoding.DecodeString(tok)
		if derr != nil {
			t.Fatalf("decode: %v", derr)
		}
		if raw[0] == 0x02 {
			v1tok = tok
			break
		}
	}
	if v1tok == "" {
		t.Skip("no 0x02-leading v1 nonce drawn in 100000 tries (astronomically unlikely)")
	}

	dek := bytesRepeat(0x66, 32)
	s := newDEKSealer(dek, []byte("ws:dddd"), legacy)
	got, err := s.Open(v1tok)
	if err != nil {
		t.Fatalf("expected v2-parse-fail then legacy fallback to succeed: %v", err)
	}
	if string(got) != secret {
		t.Fatalf("ambiguous fallback mismatch: %q", got)
	}
}
