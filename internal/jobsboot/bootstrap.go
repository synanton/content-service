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
	"github.com/synanton/content-service/internal/security"
)

// Env holds the wired dependencies for one job process.
type Env struct {
	Config  *config.Config
	Pool    *pgxpool.Pool
	Blobs   port.BlobStore
	Rows    *parquet.Reader
	Auditor *security.Auditor
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
	// Range reads need more than the BlobStore port; the guarded S3
	// backend provides them. Fail fast if a future backend does not.
	ranged, ok := blobs.(parquet.RangeGetter)
	if !ok {
		pool.Close()
		return nil, fmt.Errorf("blob store lacks range reads")
	}
	return &Env{Config: cfg, Pool: pool, Blobs: blobs, Rows: parquet.NewReader(ranged), Auditor: security.NewAuditor(cfg.Security.AuditEnabled)}, nil
}

func dialBlobs(ctx context.Context, cfg *config.Config) (port.BlobStore, error) {
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
	guarded, err := blobs.WithPolicy(policyFromConfig(cfg))
	if err != nil {
		return nil, err
	}
	if err := guarded.EnsureBucket(ctx); err != nil {
		return nil, err
	}
	return guarded, nil
}

// policyFromConfig translates the validated S3 config into the adapter
// deadline policy. Config validation already bounds every field.
func policyFromConfig(cfg *config.Config) s3adapter.Policy {
	return s3adapter.Policy{
		OpTimeout:        cfg.S3.OpTimeout,
		ListTimeout:      cfg.S3.ListTimeout,
		MaxAttempts:      cfg.S3.MaxAttempts,
		RetryBaseDelay:   cfg.S3.RetryBaseDelay,
		BreakerThreshold: cfg.S3.BreakerThreshold,
		BreakerCooldown:  cfg.S3.BreakerCooldown,
	}
}
