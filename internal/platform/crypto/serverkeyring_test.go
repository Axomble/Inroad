package crypto

import (
	"bytes"
	"testing"

	"github.com/google/uuid"
)

func testMasterKey() []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i + 1)
	}
	return k
}

func TestServerKeyringRoundTrip(t *testing.T) {
	kr, err := NewServerKeyring(testMasterKey())
	if err != nil {
		t.Fatalf("NewServerKeyring: %v", err)
	}
	user := uuid.New()
	secret := []byte("totp-secret-bytes")

	sealed, err := kr.SealerFor(user).Seal(secret)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if bytes.Contains([]byte(sealed), secret) {
		t.Fatal("sealed blob leaks the plaintext secret")
	}

	got, err := kr.SealerFor(user).Open(sealed)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(got, secret) {
		t.Fatalf("round-trip mismatch: got %q want %q", got, secret)
	}
}

// TestServerKeyringAADIsPerUser proves a secret sealed for one user cannot be
// opened under another user's context — the AAD binding fails closed.
func TestServerKeyringAADIsPerUser(t *testing.T) {
	kr, err := NewServerKeyring(testMasterKey())
	if err != nil {
		t.Fatalf("NewServerKeyring: %v", err)
	}
	a, b := uuid.New(), uuid.New()

	sealed, err := kr.SealerFor(a).Seal([]byte("secret"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := kr.SealerFor(b).Open(sealed); err == nil {
		t.Fatal("expected cross-user open to fail authentication, got nil error")
	}
}

// TestServerKeyringDistinctFromMasterAndKEK proves the derived subkey is not the
// raw master key: a blob sealed by the ServerKeyring does not open under the
// legacy master-key Sealer (domain separation via the HKDF info label).
func TestServerKeyringDistinctFromMasterKey(t *testing.T) {
	mk := testMasterKey()
	kr, err := NewServerKeyring(mk)
	if err != nil {
		t.Fatalf("NewServerKeyring: %v", err)
	}
	legacy, err := NewSealer(mk)
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}
	sealed, err := kr.SealerFor(uuid.New()).Seal([]byte("secret"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := legacy.Open(sealed); err == nil {
		t.Fatal("server-keyring blob unexpectedly opened under the raw master key")
	}
}

func TestNewServerKeyringRejectsShortKey(t *testing.T) {
	if _, err := NewServerKeyring(make([]byte, 16)); err == nil {
		t.Fatal("expected error for a 16-byte master key")
	}
}
