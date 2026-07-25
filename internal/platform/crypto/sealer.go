// Package crypto provides authenticated envelope encryption for stored credentials.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
)

// sealVersionV2 tags the per-workspace DEK envelope: base64(0x02 || nonce ||
// aes-gcm(dek, nonce, plaintext, aad)). Legacy v1 blobs carry no version byte
// (base64(nonce || ct) under the master key, nil aad) and are detected by the
// absence of this prefix.
const sealVersionV2 = 0x02

// Sealer encrypts and decrypts small secrets (OAuth tokens, SMTP passwords)
// using AES-256-GCM. Two flavours share this type:
//
//   - The legacy master-key sealer (NewSealer): v2 == false, aad == nil,
//     legacy == nil. It writes and reads the v1 format only, staying
//     byte-compatible with pre-DEK ciphertext.
//   - A per-workspace DEK sealer (newDEKSealer): v2 == true, aad bound to the
//     workspace, legacy pointing at the master-key sealer for the v1 fallback.
//     It writes v2 and reads either v2 (DEK + aad) or, on a v1 blob, delegates
//     to legacy — enabling lazy migration on the next write.
type Sealer struct {
	aead   cipher.AEAD
	aad    []byte
	legacy *Sealer
	v2     bool
}

// NewSealer builds the legacy master-key Sealer. Its Seal/Open use the v1 format
// (no version byte, nil AAD) and MUST remain byte-compatible with existing
// ciphertext. It doubles as the v1 decrypt fallback for DEK sealers.
func NewSealer(masterKey []byte) (*Sealer, error) {
	if len(masterKey) != 32 {
		return nil, fmt.Errorf("master key must be 32 bytes, got %d", len(masterKey))
	}
	block, err := aes.NewCipher(masterKey)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Sealer{aead: aead}, nil
}

// newDEKSealer builds a workspace-bound Sealer over a 32-byte DEK. aad binds the
// field ciphertext to the workspace; legacy (may be nil) opens pre-DEK v1 blobs.
// The DEK is supplied by the Keyring, which guarantees a valid 32-byte key.
func newDEKSealer(dek, aad []byte, legacy *Sealer) *Sealer {
	block, err := aes.NewCipher(dek)
	if err != nil {
		// Unreachable: the Keyring only ever hands us 32-byte DEKs. Fail loudly
		// rather than return a half-built sealer.
		panic(fmt.Sprintf("crypto: invalid DEK length: %v", err))
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		panic(fmt.Sprintf("crypto: GCM init: %v", err))
	}
	return &Sealer{aead: aead, aad: aad, legacy: legacy, v2: true}
}

// Seal encrypts plaintext. A legacy sealer emits v1 (base64(nonce||ct), nil aad);
// a DEK sealer emits v2 (base64(0x02||nonce||aes-gcm(dek,nonce,plaintext,aad))).
//
// The per-workspace DEK is long-lived and reused across seals. Each Seal draws a
// fresh random 96-bit GCM nonce, so nonce reuse only becomes a concern near the
// GCM birthday bound (~2^32 messages under one key); at expected volume (a few
// secrets per workspace) we stay far below it. DEK rotation is the mitigation
// past that point.
func (s *Sealer) Seal(plaintext []byte) (string, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	if !s.v2 {
		// v1: no version byte, nil AAD — unchanged from the pre-DEK format.
		ct := s.aead.Seal(nonce, nonce, plaintext, nil)
		return base64.StdEncoding.EncodeToString(ct), nil
	}
	buf := make([]byte, 0, 1+len(nonce)+len(plaintext)+s.aead.Overhead())
	buf = append(buf, sealVersionV2)
	buf = append(buf, nonce...)
	buf = s.aead.Seal(buf, nonce, plaintext, s.aad)
	return base64.StdEncoding.EncodeToString(buf), nil
}

// Open reverses Seal. A legacy sealer always reads v1. A DEK sealer reads v2 when
// the 0x02 prefix is present (DEK + aad); otherwise, or on a v2 auth failure with
// a legacy present, it falls back to the master-key v1 path.
func (s *Sealer) Open(token string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		return nil, err
	}
	if !s.v2 {
		return s.openV1(raw)
	}
	if len(raw) > 0 && raw[0] == sealVersionV2 {
		pt, verr := s.openV2(raw)
		if verr == nil {
			return pt, nil
		}
		// Ambiguous case: a v1 blob whose first byte happens to be 0x02 lands
		// here. Fall back to the legacy master key if available.
		if s.legacy != nil {
			if pt, lerr := s.legacy.Open(token); lerr == nil {
				return pt, nil
			}
		}
		return nil, verr
	}
	// No v2 prefix → legacy v1 under the master key.
	if s.legacy == nil {
		return nil, errors.New("crypto: legacy ciphertext but no legacy sealer configured")
	}
	return s.legacy.Open(token)
}

// openV1 decrypts base64-decoded v1 bytes (nonce || ct) with nil AAD.
func (s *Sealer) openV1(raw []byte) ([]byte, error) {
	ns := s.aead.NonceSize()
	if len(raw) < ns {
		return nil, errors.New("ciphertext too short")
	}
	return s.aead.Open(nil, raw[:ns], raw[ns:], nil)
}

// openV2 decrypts base64-decoded v2 bytes (0x02 || nonce || ct) binding s.aad.
func (s *Sealer) openV2(raw []byte) ([]byte, error) {
	body := raw[1:] // strip the version byte
	ns := s.aead.NonceSize()
	if len(body) < ns {
		return nil, errors.New("ciphertext too short")
	}
	return s.aead.Open(nil, body[:ns], body[ns:], s.aad)
}
