// Package storage provides an Object Storage provider seam for file/blob operations.
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

var (
	// ErrNotFound is returned when an object does not exist in storage.
	ErrNotFound = errors.New("storage: object not found")
)

// ObjectMetadata contains attributes describing a stored object.
type ObjectMetadata struct {
	Key          string
	Size         int64
	ContentType  string
	LastModified time.Time
	ETag         string
}

// Provider defines the object storage seam interface for managing files/blobs.
type Provider interface {
	Put(ctx context.Context, key string, body io.Reader, size int64, contentType string) error
	Get(ctx context.Context, key string) (io.ReadCloser, *ObjectMetadata, error)
	Delete(ctx context.Context, key string) error
	Exists(ctx context.Context, key string) (bool, error)
	PresignGet(ctx context.Context, key string, expires time.Duration) (string, error)
	PresignPut(ctx context.Context, key string, contentType string, expires time.Duration) (string, error)
	Name() string
}

// S3Client defines the subset of AWS S3 API operations required by S3Provider.
type S3Client interface {
	PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	DeleteObject(ctx context.Context, params *s3.DeleteObjectInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
	HeadObject(ctx context.Context, params *s3.HeadObjectInput, optFns ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
}

// S3PresignClient defines the presigning operations for S3 objects.
type S3PresignClient interface {
	PresignGetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error)
	PresignPutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error)
}

// S3Provider implements the Provider interface for AWS S3 and S3-compatible backends (MinIO, R2, Wasabi).
type S3Provider struct {
	client        S3Client
	presignClient S3PresignClient
	bucket        string
}

// NewS3Provider creates a new S3Provider for the specified bucket.
func NewS3Provider(client S3Client, presignClient S3PresignClient, bucket string) (*S3Provider, error) {
	if client == nil {
		return nil, errors.New("s3 client must not be nil")
	}
	if bucket == "" {
		return nil, errors.New("s3 bucket must not be empty")
	}
	return &S3Provider{
		client:        client,
		presignClient: presignClient,
		bucket:        bucket,
	}, nil
}

// Name returns "s3", identifying this Provider implementation.
func (p *S3Provider) Name() string {
	return "s3"
}

// Put uploads an object payload to S3.
func (p *S3Provider) Put(ctx context.Context, key string, body io.Reader, size int64, contentType string) error {
	if key == "" {
		return errors.New("storage key must not be empty")
	}
	input := &s3.PutObjectInput{
		Bucket: aws.String(p.bucket),
		Key:    aws.String(key),
		Body:   body,
	}
	if size > 0 {
		input.ContentLength = aws.Int64(size)
	}
	if contentType != "" {
		input.ContentType = aws.String(contentType)
	}

	_, err := p.client.PutObject(ctx, input)
	if err != nil {
		return fmt.Errorf("s3 put failed: %w", err)
	}
	return nil
}

// Get fetches an object from S3, returning its body stream and metadata.
func (p *S3Provider) Get(ctx context.Context, key string) (io.ReadCloser, *ObjectMetadata, error) {
	if key == "" {
		return nil, nil, errors.New("storage key must not be empty")
	}
	out, err := p.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(p.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isNotFound(err) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, fmt.Errorf("s3 get failed: %w", err)
	}

	meta := &ObjectMetadata{
		Key: key,
	}
	if out.ContentLength != nil {
		meta.Size = *out.ContentLength
	}
	if out.ContentType != nil {
		meta.ContentType = *out.ContentType
	}
	if out.LastModified != nil {
		meta.LastModified = *out.LastModified
	}
	if out.ETag != nil {
		meta.ETag = *out.ETag
	}

	return out.Body, meta, nil
}

// Delete removes an object from S3.
func (p *S3Provider) Delete(ctx context.Context, key string) error {
	if key == "" {
		return errors.New("storage key must not be empty")
	}
	_, err := p.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(p.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("s3 delete failed: %w", err)
	}
	return nil
}

// Exists checks if an object exists in S3 via HeadObject.
func (p *S3Provider) Exists(ctx context.Context, key string) (bool, error) {
	if key == "" {
		return false, errors.New("storage key must not be empty")
	}
	_, err := p.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(p.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("s3 head failed: %w", err)
	}
	return true, nil
}

// PresignGet generates a presigned URL for downloading an object.
func (p *S3Provider) PresignGet(ctx context.Context, key string, expires time.Duration) (string, error) {
	if p.presignClient == nil {
		return "", errors.New("s3 presign client is not configured")
	}
	if key == "" {
		return "", errors.New("storage key must not be empty")
	}
	req, err := p.presignClient.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(p.bucket),
		Key:    aws.String(key),
	}, func(opts *s3.PresignOptions) {
		opts.Expires = expires
	})
	if err != nil {
		return "", fmt.Errorf("s3 presign get failed: %w", err)
	}
	return req.URL, nil
}

// PresignPut generates a presigned URL for uploading an object.
func (p *S3Provider) PresignPut(ctx context.Context, key, contentType string, expires time.Duration) (string, error) {
	if p.presignClient == nil {
		return "", errors.New("s3 presign client is not configured")
	}
	if key == "" {
		return "", errors.New("storage key must not be empty")
	}
	input := &s3.PutObjectInput{
		Bucket: aws.String(p.bucket),
		Key:    aws.String(key),
	}
	if contentType != "" {
		input.ContentType = aws.String(contentType)
	}

	req, err := p.presignClient.PresignPutObject(ctx, input, func(opts *s3.PresignOptions) {
		opts.Expires = expires
	})
	if err != nil {
		return "", fmt.Errorf("s3 presign put failed: %w", err)
	}
	return req.URL, nil
}

func isNotFound(err error) bool {
	var nsk *types.NoSuchKey
	var nf *types.NotFound
	if errors.As(err, &nsk) || errors.As(err, &nf) {
		return true
	}
	return false
}
