package s3

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/synanton/content-service/internal/port"
)

// Store is a port.BlobStore backed by S3-compatible object storage.
// Keys are content-derived by the caller; this adapter never interprets them.
type Store struct {
	client *minio.Client
	bucket string
	region string
}

// Config carries the connection parameters; see internal/config.S3Config.
type Config struct {
	Endpoint   string
	Bucket     string
	Region     string
	AccessKey  string
	SecretKey  string
	UseSSL     bool
	PathStyle  bool
}

// NewStore dials the endpoint lazily: minio.New validates parameters only,
// so the first failing operation surfaces connectivity problems.
//
// The SDK's own retry budget (minio.MaxRetry, default 10) is neutralized
// to a single try: this process's only minio user is this Store, and the
// GuardedStore decorator owns the retry policy (bounded attempts inside
// the op deadline, 5xx/429/network only). Two retry owners would
// multiply attempts, inflate BB-4 latencies, and delay breaker signals.
func NewStore(cfg Config) (*Store, error) {
	if cfg.Bucket == "" {
		return nil, fmt.Errorf("s3: bucket is required")
	}
	minio.MaxRetry = 1
	lookup := minio.BucketLookupAuto
	if cfg.PathStyle {
		lookup = minio.BucketLookupPath
	}
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:        credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure:       cfg.UseSSL,
		Region:       cfg.Region,
		BucketLookup: lookup,
	})
	if err != nil {
		return nil, fmt.Errorf("s3: dial %s: %w", cfg.Endpoint, err)
	}
	return &Store{client: client, bucket: cfg.Bucket, region: cfg.Region}, nil
}

// EnsureBucket creates the bucket when missing; safe to call on every startup.
func (s *Store) EnsureBucket(ctx context.Context) error {
	found, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("s3: bucket exists check %q: %w", s.bucket, err)
	}
	if found {
		return nil
	}
	opts := minio.MakeBucketOptions{}
	if s.region != "" {
		opts.Region = s.region
	}
	if err := s.client.MakeBucket(ctx, s.bucket, opts); err != nil {
		return fmt.Errorf("s3: make bucket %q: %w", s.bucket, err)
	}
	return nil
}

// Put stores data under key with the given content type.
func (s *Store) Put(ctx context.Context, key string, data []byte, contentType string) error {
	if key == "" {
		return fmt.Errorf("s3: key is required")
	}
	if strings.HasPrefix(key, "/") {
		return fmt.Errorf("s3: key must be relative, got %q", key)
	}
	opts := minio.PutObjectOptions{ContentType: contentType}
	if opts.ContentType == "" {
		opts.ContentType = "application/octet-stream"
	}
	_, err := s.client.PutObject(ctx, s.bucket, key,
		bytes.NewReader(data), int64(len(data)), opts)
	if err != nil {
		return fmt.Errorf("s3: put %q: %w", key, err)
	}
	return nil
}

// Get fetches the whole object into memory. For streaming large payloads,
// type-assert the port.BlobStore to port.BlobReader and use GetStream.
func (s *Store) Get(ctx context.Context, key string) ([]byte, error) {
	rc, _, err := s.GetStream(ctx, key)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("s3: read %q: %w", key, err)
	}
	return data, nil
}

// GetStream opens the object for streaming reads with its size.
func (s *Store) GetStream(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	if key == "" {
		return nil, 0, fmt.Errorf("s3: key is required")
	}
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, 0, fmt.Errorf("s3: get %q: %w", key, err)
	}
	stat, err := obj.Stat()
	if err != nil {
		_ = obj.Close()
		if isNotFound(err) {
			return nil, 0, fmt.Errorf("s3: %q: %w", key, errNotFound(key))
		}
		return nil, 0, fmt.Errorf("s3: stat %q: %w", key, err)
	}
	return obj, stat.Size, nil
}

// Delete removes the object; deleting a missing key is a no-op.
func (s *Store) Delete(ctx context.Context, key string) error {
	if key == "" {
		return fmt.Errorf("s3: key is required")
	}
	err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{})
	if err != nil && !isNotFound(err) {
		return fmt.Errorf("s3: delete %q: %w", key, err)
	}
	return nil
}

// Exists reports whether the object is present.
func (s *Store) Exists(ctx context.Context, key string) (bool, error) {
	if key == "" {
		return false, fmt.Errorf("s3: key is required")
	}
	_, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("s3: stat %q: %w", key, err)
	}
	return true, nil
}

// Bucket returns the configured bucket (operators, tests).
func (s *Store) Bucket() string {
	return s.bucket
}

// Size returns the object size without fetching bytes.
func (s *Store) Size(ctx context.Context, key string) (int64, error) {
	if key == "" {
		return 0, fmt.Errorf("s3: key is required")
	}
	info, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		if isNotFound(err) {
			return 0, fmt.Errorf("s3: %q: %w", key, errNotFound(key))
		}
		return 0, fmt.Errorf("s3: stat %q: %w", key, err)
	}
	return info.Size, nil
}

// Range fetches [offset, offset+length) bytes for row-level Parquet reads.
func (s *Store) Range(ctx context.Context, key string, offset, length int64) ([]byte, error) {
	if key == "" {
		return nil, fmt.Errorf("s3: key is required")
	}
	if offset < 0 || length <= 0 {
		return nil, fmt.Errorf("s3: invalid range %d+%d for %q", offset, length, key)
	}
	opts := minio.GetObjectOptions{}
	if err := opts.SetRange(offset, offset+length-1); err != nil {
		return nil, fmt.Errorf("s3: range %q: %w", key, err)
	}
	obj, err := s.client.GetObject(ctx, s.bucket, key, opts)
	if err != nil {
		return nil, fmt.Errorf("s3: get range %q: %w", key, err)
	}
	defer obj.Close()
	data, err := io.ReadAll(obj)
	if err != nil {
		return nil, fmt.Errorf("s3: read range %q: %w", key, err)
	}
	return data, nil
}

// List enumerates objects under prefix for reconciliation scans.
func (s *Store) List(ctx context.Context, prefix string) ([]port.BlobInfo, error) {
	var out []port.BlobInfo
	for obj := range s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{
		Prefix:    prefix,
		Recursive: true,
	}) {
		if obj.Err != nil {
			return nil, fmt.Errorf("s3: list %q: %w", prefix, obj.Err)
		}
		out = append(out, port.BlobInfo{
			Key:        obj.Key,
			Size:       obj.Size,
			ModifiedAt: obj.LastModified,
		})
	}
	return out, nil
}
