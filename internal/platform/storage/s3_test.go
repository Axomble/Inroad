package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type mockS3Client struct {
	objects map[string][]byte
	meta    map[string]ObjectMetadata
}

func newMockS3Client() *mockS3Client {
	return &mockS3Client{
		objects: make(map[string][]byte),
		meta:    make(map[string]ObjectMetadata),
	}
}

func (m *mockS3Client) PutObject(_ context.Context, params *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	if params.Bucket == nil || *params.Bucket == "" {
		return nil, errors.New("missing bucket")
	}
	if params.Key == nil || *params.Key == "" {
		return nil, errors.New("missing key")
	}
	buf, err := io.ReadAll(params.Body)
	if err != nil {
		return nil, err
	}
	key := *params.Key
	m.objects[key] = buf

	cType := "application/octet-stream"
	if params.ContentType != nil {
		cType = *params.ContentType
	}
	m.meta[key] = ObjectMetadata{
		Key:          key,
		Size:         int64(len(buf)),
		ContentType:  cType,
		LastModified: time.Now(),
		ETag:         `"test-etag"`,
	}
	return &s3.PutObjectOutput{}, nil
}

func (m *mockS3Client) GetObject(_ context.Context, params *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	key := *params.Key
	data, ok := m.objects[key]
	if !ok {
		return nil, &types.NoSuchKey{}
	}
	meta := m.meta[key]
	return &s3.GetObjectOutput{
		Body:          io.NopCloser(bytes.NewReader(data)),
		ContentLength: aws.Int64(meta.Size),
		ContentType:   aws.String(meta.ContentType),
		LastModified:  aws.Time(meta.LastModified),
		ETag:          aws.String(meta.ETag),
	}, nil
}

func (m *mockS3Client) DeleteObject(_ context.Context, params *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	key := *params.Key
	delete(m.objects, key)
	delete(m.meta, key)
	return &s3.DeleteObjectOutput{}, nil
}

func (m *mockS3Client) HeadObject(_ context.Context, params *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	key := *params.Key
	meta, ok := m.meta[key]
	if !ok {
		return nil, &types.NotFound{}
	}
	return &s3.HeadObjectOutput{
		ContentLength: aws.Int64(meta.Size),
		ContentType:   aws.String(meta.ContentType),
		LastModified:  aws.Time(meta.LastModified),
		ETag:          aws.String(meta.ETag),
	}, nil
}

type mockPresignClient struct{}

func (m *mockPresignClient) PresignGetObject(_ context.Context, params *s3.GetObjectInput, _ ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error) {
	return &v4.PresignedHTTPRequest{
		URL: "https://s3.example.com/" + *params.Bucket + "/" + *params.Key + "?signed=true",
	}, nil
}

func (m *mockPresignClient) PresignPutObject(_ context.Context, params *s3.PutObjectInput, _ ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error) {
	return &v4.PresignedHTTPRequest{
		URL: "https://s3.example.com/" + *params.Bucket + "/" + *params.Key + "?upload=true",
	}, nil
}

func TestNewS3ProviderValidation(t *testing.T) {
	mock := newMockS3Client()
	if _, err := NewS3Provider(nil, nil, "my-bucket"); err == nil {
		t.Error("expected error for nil client")
	}
	if _, err := NewS3Provider(mock, nil, ""); err == nil {
		t.Error("expected error for empty bucket")
	}
}

func TestS3ProviderName(t *testing.T) {
	mock := newMockS3Client()
	p, err := NewS3Provider(mock, nil, "my-bucket")
	if err != nil {
		t.Fatalf("NewS3Provider: %v", err)
	}
	if got := p.Name(); got != "s3" {
		t.Fatalf("Name() = %q, want %q", got, "s3")
	}
}

func TestS3ProviderPutGetDelete(t *testing.T) {
	mock := newMockS3Client()
	presign := &mockPresignClient{}
	p, err := NewS3Provider(mock, presign, "my-bucket")
	if err != nil {
		t.Fatalf("NewS3Provider: %v", err)
	}

	ctx := context.Background()
	key := "avatars/user-1.jpg"
	content := []byte("fake-image-bytes")

	// 1. Put
	err = p.Put(ctx, key, bytes.NewReader(content), int64(len(content)), "image/jpeg")
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	// 2. Exists
	exists, err := p.Exists(ctx, key)
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !exists {
		t.Fatal("expected object to exist")
	}

	// 3. Get
	body, meta, err := p.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer body.Close()

	gotBytes, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(gotBytes, content) {
		t.Fatalf("content mismatch: got %s, want %s", gotBytes, content)
	}
	if meta.ContentType != "image/jpeg" {
		t.Fatalf("contentType mismatch: got %q", meta.ContentType)
	}
	if meta.Size != int64(len(content)) {
		t.Fatalf("size mismatch: got %d", meta.Size)
	}

	// 4. Presign
	presignedGet, err := p.PresignGet(ctx, key, 15*time.Minute)
	if err != nil {
		t.Fatalf("PresignGet: %v", err)
	}
	if presignedGet == "" {
		t.Fatal("expected non-empty presigned GET URL")
	}

	presignedPut, err := p.PresignPut(ctx, key, "image/jpeg", 15*time.Minute)
	if err != nil {
		t.Fatalf("PresignPut: %v", err)
	}
	if presignedPut == "" {
		t.Fatal("expected non-empty presigned PUT URL")
	}

	// 5. Delete
	err = p.Delete(ctx, key)
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// 6. Exists should now be false
	exists, err = p.Exists(ctx, key)
	if err != nil {
		t.Fatalf("Exists after delete: %v", err)
	}
	if exists {
		t.Fatal("expected object to no longer exist")
	}

	// 7. Get should return ErrNotFound
	_, _, err = p.Get(ctx, key)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestS3ProviderUnconfiguredPresigner(t *testing.T) {
	mock := newMockS3Client()
	p, err := NewS3Provider(mock, nil, "my-bucket")
	if err != nil {
		t.Fatalf("NewS3Provider: %v", err)
	}

	ctx := context.Background()
	if _, err := p.PresignGet(ctx, "key", time.Minute); err == nil {
		t.Error("expected error when presignClient is nil")
	}
	if _, err := p.PresignPut(ctx, "key", "text/plain", time.Minute); err == nil {
		t.Error("expected error when presignClient is nil")
	}
}
