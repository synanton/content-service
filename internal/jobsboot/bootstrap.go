// Package jobsboot wires background-job binaries: config, Postgres pool,
// and the S3 blob store. Jobs never run migrations; the content-api
// service owns the schema, and jobs fail fast when it is missing.
package jobsboot

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/synanton/content-service/internal/adapter/parquet"
	s3adapter "github.com/synanton/content-service/internal/adapter/s3"
	"github.com/synanton/content-service/internal/config"
	"github.com/synanton/content-service/internal/logging"
	"github.com/synanton/content-service/internal/port"
)

// Env holds the wired dependencies for one job process.
type Env struct {
	Config *config.Config
	Pool   *pgxpool.Pool
	Blobs  port.BlobStore
	Rows   *parquet.Reader
}

// Setup loads config, connects Postgres, pings it, and ensures the bucket.
// The caller owns Pool.Close.
func Setup(ctx context.Context) (*Env, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	// Process-wide JSON logging is configured here so every job binary
	// shares one stream and level without repeating the wiring.
	logging.SetupDefault(cfg.Observability.LogLevel)
	pool, err := pgxpool.New(ctx, cfg.DB.URL)
	if err != nil {
		return nil, fmt.Errorf("db connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db ping: %w", err)
	}
	blobs, err := dialBlobs(ctx, cfg)
	if err != nil {
		pool.Close()
		return nil, err
	}
	return &Env{Config: cfg, Pool: pool, Blobs: blobs, Rows: parquet.NewReader(blobs)}, nil
}

func dialBlobs(ctx context.Context, cfg *config.Config) (*s3adapter.Store, error) {
	endpoint := cfg.S3.Endpoint
	secure := strings.HasPrefix(endpoint, "https://")
	endpoint = strings.TrimPrefix(strings.TrimPrefix(endpoint, "https://"), "http://")
	blobs, err := s3adapter.NewStore(s3adapter.Config{
		Endpoint:  endpoint,
		Bucket:    cfg.S3.Bucket,
		Region:    cfg.S3.Region,
		AccessKey: cfg.S3.AccessKey,
		SecretKey: cfg.S3.SecretKey,
		UseSSL:    secure,
		PathStyle: cfg.S3.UsePathStyle,
	})
	if err != nil {
		return nil, err
	}
	if err := blobs.EnsureBucket(ctx); err != nil {
		return nil, err
	}
	return blobs, nil
}
