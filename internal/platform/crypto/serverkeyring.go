package crypto

import (
	"crypto/hkdf"
	"crypto/sha256"
	"fmt"

	"github.com/google/uuid"
)

// userSecretInfo domain-separates the server-level user-secret subkey from the
// KEK subkey (kekInfo) and the raw master key. Deriving under a distinct HKDF
// info label guarantees the user-secret sealer never shares a key/nonce domain
// with per-workspace DEK wrapping — the same discipline LocalKeyProvider uses.
const userSecretInfo = "inroad-user-secret-v1"

// ServerKeyring seals USER-level secrets — today a user's TOTP secret — under a
// single server-level key derived from the master key, NOT a per-workspace DEK.
//
// A TOTP secret belongs to a human across EVERY workspace they are a member of;
// no single workspace owns it, so the per-workspace Keyring.SealerFor(ctx, ws)
// DEK model (whose ciphertext is AAD-bound to one workspace and crypto-shredded
// when that workspace is deleted) does not fit. The subkey is HKDF-SHA256-derived
// from INROAD_MASTER_KEY under userSecretInfo, mirroring how the KEK subkey is
// derived, so losing the master key loses these secrets too — no wider blast
// radius than the existing scheme.
type ServerKeyring struct {
	subkey []byte
}

// NewServerKeyring derives the server-level user-secret subkey from the 32-byte
// master key.
func NewServerKeyring(masterKey []byte) (*ServerKeyring, error) {
	if len(masterKey) != 32 {
		return nil, fmt.Errorf("master key must be 32 bytes, got %d", len(masterKey))
	}
	subkey, err := hkdf.Key(sha256.New, masterKey, nil /*salt*/, userSecretInfo, 32)
	if err != nil {
		return nil, err
	}
	return &ServerKeyring{subkey: subkey}, nil
}

// userAAD binds a sealed user secret to its owning user: a ciphertext minted for
// one user fails authentication if presented under another user's context, so a
// row swapped between users decrypts to an error rather than silently to garbage.
func userAAD(userID uuid.UUID) []byte {
	return []byte("user:" + userID.String())
}

// SealerFor returns a Sealer bound to userID's AAD. It writes the v2 envelope
// (AES-256-GCM, AAD-bound) and carries no legacy fallback: user secrets are
// introduced with this scheme and have no pre-existing v1 ciphertext.
func (k *ServerKeyring) SealerFor(userID uuid.UUID) *Sealer {
	return newDEKSealer(k.subkey, userAAD(userID), nil)
}
