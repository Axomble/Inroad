package storage

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/inroad/inroad/internal/platform/config"
)

// The factory returns the filesystem provider by default (no S3 bucket
// configured) and the S3 provider once one is.
func TestFromEnvDefaultsToFilesystem(t *testing.T) {
	cfg := &config.Config{StorageFSRoot: t.TempDir()}
	p, err := FromEnv(context.Background(), cfg)
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if got := p.Name(); got != "fs" {
		t.Fatalf("Name() = %q, want %q (no S3 bucket configured)", got, "fs")
	}
}

// INROAD_S3_ENDPOINT is operator-supplied and goes straight onto the SDK client
// as its BaseEndpoint, so an http:// value would send SigV4-signed requests and
// object bodies in the clear. Refused at construction, with the same shape the
// repo's other cleartext decision uses (invariant 6): TLS is the default,
// cleartext has to be CHOSEN, and anything other than an explicit opt-out keeps
// TLS mandatory.
func TestFromEnvRefusesAPlaintextS3Endpoint(t *testing.T) {
	for name, endpoint := range map[string]string{
		"http":            "http://minio.internal:9000",
		"upper-case http": "HTTP://minio.internal:9000",
		"no scheme":       "minio.internal:9000",
		"other scheme":    "ftp://minio.internal",
	} {
		t.Run(name, func(t *testing.T) {
			cfg := &config.Config{
				StorageFSRoot: t.TempDir(), StorageS3Bucket: "b",
				StorageS3Region: "us-east-1", StorageS3Endpoint: endpoint,
			}
			p, err := FromEnv(context.Background(), cfg)
			if !errors.Is(err, ErrInsecureEndpoint) {
				t.Fatalf("FromEnv err = %v, want ErrInsecureEndpoint", err)
			}
			if p != nil {
				t.Error("a refused endpoint must not yield a usable provider")
			}
			// A startup error an operator can act on names the variable.
			if !strings.Contains(err.Error(), "INROAD_S3_ALLOW_PLAINTEXT_ENDPOINT") {
				t.Errorf("err = %q, want it to name the opt-out variable", err)
			}
		})
	}
}

// The opt-out, explicit and persisted in config the same way the mailbox TLS
// one is. It exists for a MinIO in a private network during development; a
// misspelled or absent value keeps TLS mandatory.
func TestFromEnvAllowsAPlaintextS3EndpointOnlyWithTheExplicitOptOut(t *testing.T) {
	cfg := &config.Config{
		StorageFSRoot: t.TempDir(), StorageS3Bucket: "b", StorageS3Region: "us-east-1",
		StorageS3Endpoint: "http://minio.internal:9000", StorageS3AllowPlaintextEndpoint: true,
	}
	p, err := FromEnv(context.Background(), cfg)
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if got := p.Name(); got != "s3" {
		t.Fatalf("Name() = %q, want s3", got)
	}
}

// An https endpoint, and the empty endpoint that means real AWS S3, both pass
// without any opt-out — the guard must not make the ordinary case harder.
func TestFromEnvAcceptsHTTPSAndTheDefaultAWSEndpoint(t *testing.T) {
	for name, endpoint := range map[string]string{
		"https":            "https://minio.internal:9000",
		"upper-case https": "HTTPS://minio.internal:9000",
		"unset (real AWS)": "",
	} {
		t.Run(name, func(t *testing.T) {
			cfg := &config.Config{
				StorageFSRoot: t.TempDir(), StorageS3Bucket: "b",
				StorageS3Region: "us-east-1", StorageS3Endpoint: endpoint,
			}
			if _, err := FromEnv(context.Background(), cfg); err != nil {
				t.Fatalf("FromEnv: %v", err)
			}
		})
	}
}

func TestFromEnvSelectsS3WhenBucketConfigured(t *testing.T) {
	cfg := &config.Config{
		StorageFSRoot:            t.TempDir(),
		StorageS3Bucket:          "my-bucket",
		StorageS3Region:          "us-east-1",
		StorageS3AccessKeyID:     "AKIAEXAMPLE",
		StorageS3SecretAccessKey: "secret",
	}
	p, err := FromEnv(context.Background(), cfg)
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if got := p.Name(); got != "s3" {
		t.Fatalf("Name() = %q, want %q (bucket configured)", got, "s3")
	}
}
