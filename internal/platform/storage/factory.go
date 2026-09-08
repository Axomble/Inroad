package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"

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

// ErrInsecureEndpoint is a configured INROAD_S3_ENDPOINT that is not https and
// carries no explicit cleartext opt-out. It is a STARTUP error: the endpoint is
// operator-supplied configuration, so the right time and place to say so is
// before the process serves anything, not on the first upload.
var ErrInsecureEndpoint = errors.New("storage: S3 endpoint must use https")

// newS3ProviderFromEnv builds the AWS S3 client(s) S3Provider needs. Region
// and credentials are resolved through the AWS SDK's own LoadDefaultConfig so
// a deployment that already has AWS_* env vars, a shared config file, or an
// attached IAM role needs no INROAD-specific credential configuration at all
// — StorageS3AccessKeyID only overrides that default when a self-hoster's
// backend (MinIO, R2, Wasabi) has no such chain to draw from.
func newS3ProviderFromEnv(ctx context.Context, cfg *config.Config) (Provider, error) {
	if err := checkEndpointScheme(cfg.StorageS3Endpoint, cfg.StorageS3AllowPlaintextEndpoint); err != nil {
		return nil, err
	}
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

// checkEndpointScheme refuses a custom endpoint that would be dialled in the
// clear.
//
// An empty endpoint is real AWS S3 and is always https — the SDK builds that
// URL itself, so there is nothing here to check. A CUSTOM endpoint goes
// straight onto the client as BaseEndpoint, and an http:// one would send every
// SigV4-signed request and every object body in cleartext: the presigned GET/PUT
// URLs this provider hands out would be cleartext too, and those are the ones
// that leave the deployment.
//
// The shape follows invariant 6's rule for the mailbox and transactional SMTP
// legs rather than inventing a second one: TLS is the default, cleartext must be
// CHOSEN through an explicit persisted opt-out, and anything that is not that
// opt-out — unset, empty, misspelled — keeps TLS mandatory. A missing scheme is
// refused rather than assumed https, because assuming is how a typo becomes a
// silent downgrade.
func checkEndpointScheme(endpoint string, allowPlaintext bool) error {
	if endpoint == "" || allowPlaintext {
		return nil
	}
	if strings.HasPrefix(strings.ToLower(endpoint), "https://") {
		return nil
	}
	return fmt.Errorf("%w: INROAD_S3_ENDPOINT=%q would send signed requests and object bodies in the clear; "+
		"use https, or set INROAD_S3_ALLOW_PLAINTEXT_ENDPOINT=true to accept that deliberately",
		ErrInsecureEndpoint, endpoint)
}
