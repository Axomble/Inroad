package crypto

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/kms"
)

type fakeKMSClient struct {
	failEncrypt bool
	failDecrypt bool
}

func (f *fakeKMSClient) Encrypt(_ context.Context, params *kms.EncryptInput, _ ...func(*kms.Options)) (*kms.EncryptOutput, error) {
	if f.failEncrypt {
		return nil, errors.New("kms service unavailable")
	}
	if len(params.Plaintext) == 0 {
		return nil, errors.New("empty plaintext")
	}
	// Prefix with "kms:" and append aad string if present
	aadStr := ""
	if params.EncryptionContext != nil {
		aadStr = params.EncryptionContext["aad"]
	}
	ct := append([]byte("kms:"+aadStr+":"), params.Plaintext...)
	return &kms.EncryptOutput{
		CiphertextBlob: ct,
	}, nil
}

func (f *fakeKMSClient) Decrypt(_ context.Context, params *kms.DecryptInput, _ ...func(*kms.Options)) (*kms.DecryptOutput, error) {
	if f.failDecrypt {
		return nil, errors.New("kms service unavailable")
	}
	aadStr := ""
	if params.EncryptionContext != nil {
		aadStr = params.EncryptionContext["aad"]
	}
	prefix := []byte("kms:" + aadStr + ":")
	if !bytes.HasPrefix(params.CiphertextBlob, prefix) {
		return nil, fmt.Errorf("invalid ciphertext or aad mismatch")
	}
	pt := bytes.TrimPrefix(params.CiphertextBlob, prefix)
	return &kms.DecryptOutput{
		Plaintext: pt,
	}, nil
}

func TestNewKMSKeyProviderValidation(t *testing.T) {
	fake := &fakeKMSClient{}
	if _, err := NewKMSKeyProvider(nil, "key-123"); err == nil {
		t.Error("expected error when client is nil")
	}
	if _, err := NewKMSKeyProvider(fake, ""); err == nil {
		t.Error("expected error when keyID is empty")
	}
}

func TestKMSKeyProviderName(t *testing.T) {
	fake := &fakeKMSClient{}
	p, err := NewKMSKeyProvider(fake, "alias/inroad-kek")
	if err != nil {
		t.Fatalf("NewKMSKeyProvider: %v", err)
	}
	if got := p.Name(); got != "aws-kms" {
		t.Fatalf("Name() = %q, want %q", got, "aws-kms")
	}
}

func TestKMSKeyProviderWrapUnwrapRoundTrip(t *testing.T) {
	fake := &fakeKMSClient{}
	p, err := NewKMSKeyProvider(fake, "arn:aws:kms:us-east-1:123456789012:key/test-key")
	if err != nil {
		t.Fatalf("NewKMSKeyProvider: %v", err)
	}

	ctx := context.Background()
	dek := dek32()
	aad := []byte("ws:1234-5678")

	wrapped, err := p.Wrap(ctx, dek, aad)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if bytes.Equal(wrapped, dek) {
		t.Fatal("wrapped blob matches plaintext DEK")
	}

	got, err := p.Unwrap(ctx, wrapped, aad)
	if err != nil {
		t.Fatalf("Unwrap: %v", err)
	}
	if !bytes.Equal(got, dek) {
		t.Fatalf("Unwrap mismatch: got %x, want %x", got, dek)
	}
}

func TestKMSKeyProviderAADBinding(t *testing.T) {
	fake := &fakeKMSClient{}
	p, err := NewKMSKeyProvider(fake, "key-123")
	if err != nil {
		t.Fatalf("NewKMSKeyProvider: %v", err)
	}

	ctx := context.Background()
	dek := dek32()
	aadA := []byte("ws:tenant-a")
	aadB := []byte("ws:tenant-b")

	wrapped, err := p.Wrap(ctx, dek, aadA)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}

	if _, err := p.Unwrap(ctx, wrapped, aadB); err == nil {
		t.Fatal("expected Unwrap to fail with mismatched aad")
	}
}

func TestKMSKeyProviderErrors(t *testing.T) {
	fake := &fakeKMSClient{failEncrypt: true}
	p, err := NewKMSKeyProvider(fake, "key-123")
	if err != nil {
		t.Fatalf("NewKMSKeyProvider: %v", err)
	}

	ctx := context.Background()
	if _, err := p.Wrap(ctx, dek32(), nil); err == nil {
		t.Fatal("expected Wrap to fail when KMS Encrypt fails")
	}

	if _, err := p.Wrap(ctx, nil, nil); err == nil {
		t.Fatal("expected Wrap to fail on empty DEK")
	}

	if _, err := p.Unwrap(ctx, nil, nil); err == nil {
		t.Fatal("expected Unwrap to fail on empty wrapped blob")
	}

	fake.failEncrypt = false
	fake.failDecrypt = true
	wrapped, _ := p.Wrap(ctx, dek32(), nil)
	if _, err := p.Unwrap(ctx, wrapped, nil); err == nil {
		t.Fatal("expected Unwrap to fail when KMS Decrypt fails")
	}
}
