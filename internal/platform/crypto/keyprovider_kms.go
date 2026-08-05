package crypto

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
)

// KMSClient defines the subset of AWS KMS API operations used by KMSKeyProvider.
// This interface enables mocking AWS KMS in unit tests without live network calls.
type KMSClient interface {
	Encrypt(ctx context.Context, params *kms.EncryptInput, optFns ...func(*kms.Options)) (*kms.EncryptOutput, error)
	Decrypt(ctx context.Context, params *kms.DecryptInput, optFns ...func(*kms.Options)) (*kms.DecryptOutput, error)
}

// KMSKeyProvider implements KeyProvider using AWS KMS for key-encryption-key (KEK) envelope encryption.
type KMSKeyProvider struct {
	client KMSClient
	keyID  string
}

// NewKMSKeyProvider creates a new KMSKeyProvider using the given KMS API client and key ID/ARN.
func NewKMSKeyProvider(client KMSClient, keyID string) (*KMSKeyProvider, error) {
	if client == nil {
		return nil, errors.New("kms client must not be nil")
	}
	if keyID == "" {
		return nil, errors.New("kms keyID must not be empty")
	}
	return &KMSKeyProvider{
		client: client,
		keyID:  keyID,
	}, nil
}

// Name returns "aws-kms", identifying this KeyProvider implementation.
func (p *KMSKeyProvider) Name() string {
	return "aws-kms"
}

// Wrap encrypts a plaintext DEK via AWS KMS Encrypt. aad (if non-empty) is passed as EncryptionContext.
func (p *KMSKeyProvider) Wrap(ctx context.Context, dek, aad []byte) ([]byte, error) {
	if len(dek) == 0 {
		return nil, errors.New("dek must not be empty")
	}
	var encCtx map[string]string
	if len(aad) > 0 {
		encCtx = map[string]string{"aad": string(aad)}
	}

	out, err := p.client.Encrypt(ctx, &kms.EncryptInput{
		KeyId:             aws.String(p.keyID),
		Plaintext:         dek,
		EncryptionContext: encCtx,
	})
	if err != nil {
		return nil, fmt.Errorf("kms encrypt failed: %w", err)
	}
	return out.CiphertextBlob, nil
}

// Unwrap decrypts a KMS-wrapped DEK via AWS KMS Decrypt. The exact same aad used during Wrap must be provided.
func (p *KMSKeyProvider) Unwrap(ctx context.Context, wrapped, aad []byte) ([]byte, error) {
	if len(wrapped) == 0 {
		return nil, errors.New("wrapped dek must not be empty")
	}
	var encCtx map[string]string
	if len(aad) > 0 {
		encCtx = map[string]string{"aad": string(aad)}
	}

	out, err := p.client.Decrypt(ctx, &kms.DecryptInput{
		KeyId:             aws.String(p.keyID),
		CiphertextBlob:    wrapped,
		EncryptionContext: encCtx,
	})
	if err != nil {
		return nil, fmt.Errorf("kms decrypt failed: %w", err)
	}
	return out.Plaintext, nil
}
