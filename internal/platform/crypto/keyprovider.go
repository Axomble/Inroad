package crypto

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
)

// KeyProvider is the key-encryption-key (KEK) seam: it wraps and unwraps
// per-workspace data-encryption keys (DEKs). LocalKeyProvider is the default
// (AES-256-GCM under a subkey derived from INROAD_MASTER_KEY); a cloud KMS is a
// future drop-in.
//
// aad is additional authenticated data that binds the wrapped DEK to a context
// (e.g. the workspace id). It is authenticated but not encrypted, and the exact
// same aad must be supplied to Unwrap or authentication fails. It maps to a
// cloud KMS EncryptionContext. aad may be nil.
type KeyProvider interface {
	Wrap(ctx context.Context, dek []byte, aad []byte) (wrapped []byte, err error)
	Unwrap(ctx context.Context, wrapped []byte, aad []byte) (dek []byte, err error)
	Name() string
}

// kekInfo is the HKDF info string that domain-separates the KEK subkey from the
// raw master key.
const kekInfo = "inroad-kek-v1"

// LocalKeyProvider wraps DEKs with AES-256-GCM under a KEK subkey derived from
// the local master key.
type LocalKeyProvider struct{ aead cipher.AEAD }

func NewLocalKeyProvider(masterKey []byte) (*LocalKeyProvider, error) {
	if len(masterKey) != 32 {
		return nil, fmt.Errorf("master key must be 32 bytes, got %d", len(masterKey))
	}
	// The raw master key remains the legacy v1 field key (decrypt-only). Derive
	// a distinct HKDF-SHA256 subkey for the KEK that wraps DEKs so the two never
	// share a key/nonce domain — domain separation.
	kek, err := hkdf.Key(sha256.New, masterKey, nil /*salt*/, kekInfo, 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(kek)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &LocalKeyProvider{aead: aead}, nil
}

func (p *LocalKeyProvider) Name() string { return "local" }

func (p *LocalKeyProvider) Wrap(_ context.Context, dek []byte, aad []byte) ([]byte, error) {
	nonce := make([]byte, p.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return p.aead.Seal(nonce, nonce, dek, aad), nil
}

func (p *LocalKeyProvider) Unwrap(_ context.Context, wrapped []byte, aad []byte) ([]byte, error) {
	ns := p.aead.NonceSize()
	if len(wrapped) < ns {
		return nil, errors.New("wrapped dek too short")
	}
	return p.aead.Open(nil, wrapped[:ns], wrapped[ns:], aad)
}
