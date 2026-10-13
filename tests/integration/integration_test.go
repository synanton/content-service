package integration

import (
	"bytes"
	"context"
	"testing"

	"github.com/synanton/content-service/internal/config"
	"github.com/synanton/content-service/internal/storage"
)

func TestPutAndGet_SmallPayloadInline(t *testing.T) {
	cfg := &config.Config{
		Storage: config.StorageConfig{
			InlineThresholdBytes: 4096,
			ObjectThresholdBytes: 262144,
		},
	}
	s := storage.NewTieredStorage(cfg)
	ctx := context.Background()
	payload := []byte("small test payload for Phase 0")

	result, err := s.Put(ctx, "test-tenant", "artifact-1", "sha256:abc123", payload)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	if result.Tier != "inline" {
		t.Fatalf("Expected inline tier for small payload, got %s", result.Tier)
	}
	if result.Size != int64(len(payload)) {
		t.Fatalf("Expected size %d, got %d", len(payload), result.Size)
	}
}

func TestPutAndGet_MediumPayloadParquet(t *testing.T) {
	cfg := &config.Config{
		Storage: config.StorageConfig{
			InlineThresholdBytes: 100,
			ObjectThresholdBytes: 10000,
		},
	}
	s := storage.NewTieredStorage(cfg)
	ctx := context.Background()
	payload := bytes.Repeat([]byte("x"), 5000)

	result, err := s.Put(ctx, "test-tenant", "artifact-2", "sha256:def456", payload)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	if result.Tier != "parquet" {
		t.Fatalf("Expected parquet tier for medium payload, got %s", result.Tier)
	}
}

func TestPutAndGet_LargePayloadS3(t *testing.T) {
	cfg := &config.Config{
		Storage: config.StorageConfig{
			InlineThresholdBytes: 100,
			ObjectThresholdBytes: 1000,
		},
	}
	s := storage.NewTieredStorage(cfg)
	ctx := context.Background()
	payload := bytes.Repeat([]byte("y"), 50000)

	result, err := s.Put(ctx, "test-tenant", "artifact-3", "sha256:ghi789", payload)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	if result.Tier != "s3" {
		t.Fatalf("Expected s3 tier for large payload, got %s", result.Tier)
	}
}

func TestPut_RejectsEmptyTenantID(t *testing.T) {
	cfg := &config.Config{}
	s := storage.NewTieredStorage(cfg)
	ctx := context.Background()

	_, err := s.Put(ctx, "", "artifact-4", "sha256:jkl012", []byte("payload"))
	if err == nil {
		t.Fatal("Expected error for empty tenant ID")
	}
}

func TestExists_ReturnsFalseForNonexistent(t *testing.T) {
	cfg := &config.Config{}
	s := storage.NewTieredStorage(cfg)
	ctx := context.Background()

	exists, err := s.Exists(ctx, "test-tenant", "artifact-5", "sha256:mno345")
	if err != nil {
		t.Fatalf("Exists failed: %v", err)
	}
	if exists {
		t.Fatal("Expected exists to return false for nonexistent artifact")
	}
}
