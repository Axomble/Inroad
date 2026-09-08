package storage

import (
	"context"
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
