package storage

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/inroad/inroad/internal/platform/config"
)

// FromEnv selects and builds the Provider this deployment should use, from
// already-loaded config — the same "from-env factory, local default, cloud
// opt-in" shape internal/platform/redisconn.Parse and
// internal/platform/crypto's key-provider selection (internal/platform/keys.
// BuildKeyring) use: the seam type itself (Provider, KeyProvider) stays
// config-agnostic, and one small function reads the already-validated config
// and decides which concrete implementation to hand back.
//
// Filesystem is the default (INROAD_S3_BUCKET unset) so every self-host works
// with zero storage configuration; S3 (or an S3-compatible endpoint) is the
// opt-in. See the package doc for why nothing calls this yet.
func FromEnv(ctx context.Context, cfg *config.Config) (Provider, error) {
	if cfg.StorageS3Bucket == "" {
		return NewFSProvider(cfg.StorageFSRoot)
	}
	return newS3ProviderFromEnv(ctx, cfg)
}

// newS3ProviderFromEnv builds the AWS S3 client(s) S3Provider needs. Region
// and credentials are resolved through the AWS SDK's own LoadDefaultConfig so
// a deployment that already has AWS_* env vars, a shared config file, or an
// attached IAM role needs no INROAD-specific credential configuration at all
// — StorageS3AccessKeyID only overrides that default when a self-hoster's
// backend (MinIO, R2, Wasabi) has no such chain to draw from.
func newS3ProviderFromEnv(ctx context.Context, cfg *config.Config) (Provider, error) {
	optFns := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(cfg.StorageS3Region)}
	if cfg.StorageS3AccessKeyID != "" {
		optFns = append(optFns, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.StorageS3AccessKeyID, cfg.StorageS3SecretAccessKey, "")))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, optFns...)
	if err != nil {
		return nil, fmt.Errorf("storage: load AWS config: %w", err)
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.StorageS3Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.StorageS3Endpoint)
		}
		o.UsePathStyle = cfg.StorageS3ForcePathStyle
	})
	return NewS3Provider(client, s3.NewPresignClient(client), cfg.StorageS3Bucket)
}
