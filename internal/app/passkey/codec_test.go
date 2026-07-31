package passkey

import (
	"testing"

	"github.com/go-webauthn/webauthn/protocol"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

func TestTransportsRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		in   []protocol.AuthenticatorTransport
		enc  string
	}{
		{"empty", nil, ""},
		{"single", []protocol.AuthenticatorTransport{"internal"}, "internal"},
		{"multi", []protocol.AuthenticatorTransport{"internal", "hybrid"}, "internal,hybrid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := encodeTransports(tc.in)
			if got != tc.enc {
				t.Fatalf("encode = %q, want %q", got, tc.enc)
			}
			back := decodeTransports(got)
			if len(back) != len(tc.in) {
				t.Fatalf("decode len = %d, want %d", len(back), len(tc.in))
			}
			for i := range back {
				if back[i] != tc.in[i] {
					t.Fatalf("decode[%d] = %q, want %q", i, back[i], tc.in[i])
				}
			}
		})
	}
}

func TestDecodeTransportsDropsEmptySegments(t *testing.T) {
	got := decodeTransports("internal,,usb")
	if len(got) != 2 || got[0] != "internal" || got[1] != "usb" {
		t.Fatalf("decode dropped-empty = %+v, want [internal usb]", got)
	}
}

// TestToLibCredentialRestoresVerificationState proves every field the login
// verifier reads is rebuilt from the stored row: the public key + id, the signature
// counter (drives clone detection), the AAGUID, transports, and the backup flags
// (the library rejects a login whose backup-eligible flag disagrees with registration).
func TestToLibCredentialRestoresVerificationState(t *testing.T) {
	row := gen.WebauthnCredential{
		CredentialID:    []byte{1, 2, 3, 4},
		PublicKey:       []byte{5, 6, 7, 8},
		SignCount:       42,
		Aaguid:          []byte{9, 9},
		Transports:      "internal,hybrid",
		AttestationType: "none",
		BackupEligible:  true,
		BackupState:     true,
	}
	c := toLibCredential(row)

	if string(c.ID) != string(row.CredentialID) || string(c.PublicKey) != string(row.PublicKey) {
		t.Fatalf("id/public key not restored")
	}
	if c.Authenticator.SignCount != 42 {
		t.Fatalf("sign count = %d, want 42", c.Authenticator.SignCount)
	}
	if string(c.Authenticator.AAGUID) != string(row.Aaguid) {
		t.Fatalf("aaguid not restored")
	}
	if !c.Flags.BackupEligible || !c.Flags.BackupState {
		t.Fatalf("backup flags not restored: %+v", c.Flags)
	}
	if len(c.Transport) != 2 {
		t.Fatalf("transports not restored: %+v", c.Transport)
	}
}
