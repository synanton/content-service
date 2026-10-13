package storage

import (
	"context"
	"fmt"

	"github.com/synanton/content-service/internal/config"
)

type PutResult struct {
	Digest string
	Size   int64
	Tier   string
}

type GetResult struct {
	Data []byte
	Size int64
	Digest string
}

type Storage interface {
	Put(ctx context.Context, tenantID, artifactID, digest string, data []byte) (*PutResult, error)
	Get(ctx context.Context, tenantID, artifactID, digest string) (*GetResult, error)
	Delete(ctx context.Context, tenantID, artifactID, digest string) error
	Exists(ctx context.Context, tenantID, artifactID, digest string) (bool, error)
}

type tieredStorage struct {
	cfg     *config.Config
	metrics *Metrics
}

func NewTieredStorage(cfg *config.Config) Storage {
	return &tieredStorage{
		cfg:     cfg,
		metrics: NewMetrics(),
	}
}

func (s *tieredStorage) selectTier(size int64) string {
	if size < int64(s.cfg.Storage.InlineThresholdBytes) {
		return "inline"
	}
	if size < int64(s.cfg.Storage.ObjectThresholdBytes) {
		return "parquet"
	}
	return "s3"
}

func (s *tieredStorage) Put(ctx context.Context, tenantID, artifactID, digest string, data []byte) (*PutResult, error) {
	if tenantID == "" {
		return nil, fmt.Errorf("tenant_id is required")
	}
	if digest == "" {
		return nil, fmt.Errorf("digest is required")
	}

	tier := s.selectTier(int64(len(data)))
	result := &PutResult{
		Digest: digest,
		Size:   int64(len(data)),
		Tier:   tier,
	}

	s.metrics.RecordPut(tier, result.Size)

	return result, nil
}

func (s *tieredStorage) Get(ctx context.Context, tenantID, artifactID, digest string) (*GetResult, error) {
	if tenantID == "" {
		return nil, fmt.Errorf("tenant_id is required")
	}
	if digest == "" {
		return nil, fmt.Errorf("digest is required")
	}

	return nil, fmt.Errorf("not implemented in Phase 0 skeleton")
}

func (s *tieredStorage) Delete(ctx context.Context, tenantID, artifactID, digest string) error {
	if tenantID == "" {
		return fmt.Errorf("tenant_id is required")
	}
	return nil
}

func (s *tieredStorage) Exists(ctx context.Context, tenantID, artifactID, digest string) (bool, error) {
	if tenantID == "" {
		return false, fmt.Errorf("tenant_id is required")
	}
	return false, nil
}
