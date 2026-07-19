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

// Sealer encrypts and decrypts small secrets (OAuth tokens, SMTP passwords)
// using AES-256-GCM under a single master key. Per-tenant data keys are a
// future extension; the interface stays the same.
type Sealer struct {
	aead cipher.AEAD
}

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

// Seal returns base64(nonce || ciphertext).
func (s *Sealer) Seal(plaintext []byte) (string, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ct := s.aead.Seal(nonce, nonce, plaintext, nil)
	return base64.StdEncoding.EncodeToString(ct), nil
}

// Open reverses Seal.
func (s *Sealer) Open(token string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		return nil, err
	}
	ns := s.aead.NonceSize()
	if len(raw) < ns {
		return nil, errors.New("ciphertext too short")
	}
	nonce, ct := raw[:ns], raw[ns:]
	return s.aead.Open(nil, nonce, ct, nil)
}
