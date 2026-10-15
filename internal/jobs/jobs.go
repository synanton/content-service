package jobs

import (
	"context"
	"fmt"
	"hash/fnv"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ExpiredCandidate is an artifact eligible for physical disposition:
// fully expired beyond grace, no legal hold, no live references.
type ExpiredCandidate struct {
	TenantID    string
	ArtifactID  string
	Digest      string
	StorageTier string
}

// ExternalArtifact is an artifact whose bytes live in object storage.
type ExternalArtifact struct {
	TenantID    string
	ArtifactID  string
	Digest      string
	StorageTier string
}

// ParquetCandidate is a parquet-tier artifact awaiting compaction.
type ParquetCandidate struct {
	TenantID   string
	ArtifactID string
	SizeBytes  int64
}

// LifecycleStore is the job-facing slice of the metadata database.
// Implemented by the postgres adapter; faked in job unit tests.
type LifecycleStore interface {
	ListDispositionCandidates(ctx context.Context, cutoff time.Time, limit int) ([]ExpiredCandidate, error)
	DisposeArtifact(ctx context.Context, tenantID, artifactID, actor string) (DisposedArtifact, bool, error)
	ListTenants(ctx context.Context) ([]string, error)
	ListArtifactIDs(ctx context.Context, tenantID string, limit int, sampleRate float64) ([]string, error)
	RecordIntegrityFailure(ctx context.Context, tenantID, artifactID, detail string) error
	ListExternalArtifacts(ctx context.Context, limit int) ([]ExternalArtifact, error)
	ArtifactExistsByDigest(ctx context.Context, tenantID, digest string) (bool, error)
	ListParquetCandidates(ctx context.Context, limit int) ([]ParquetCandidate, error)
	ListSmallParquetFiles(ctx context.Context, tenantID string, maxRows, limit int) ([]ParquetFile, error)
	InsertParquetFile(ctx context.Context, file ParquetFile) error
	SwapParquetFilePointers(ctx context.Context, tenantID, oldFileID, newFileID string, rowOffset int64) error
	DeleteParquetFile(ctx context.Context, fileID string) error
	ParquetFileKeyExists(ctx context.Context, key string) (bool, error)
}

// DisposedArtifact describes a physically disposed artifact for blob cleanup.
type DisposedArtifact struct {
	Digest   string
	Tier     string
	FileKey  string
}

// ParquetFile is one immutable Parquet file tracked in metadata.
type ParquetFile struct {
	FileID string
	Tenant string
	Key    string
	Rows   int
	Bytes  int64
}

// Runner executes a job immediately and then on every tick until ctx ends.
// A single Run error is logged, never fatal: the next tick retries.
type Runner struct {
	Name     string
	Interval time.Duration
	Run      func(context.Context) error
}

// Loop blocks until ctx is done.
func (r Runner) Loop(ctx context.Context) {
	slog.Info("job starting", "job", r.Name, "interval", r.Interval)
	if err := r.runOnce(ctx); err != nil && ctx.Err() == nil {
		slog.Error("job initial run failed", "job", r.Name, "err", err)
	}
	ticker := time.NewTicker(r.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("job stopping", "job", r.Name)
			return
		case at := <-ticker.C:
			_ = at
			if err := r.runOnce(ctx); err != nil && ctx.Err() == nil {
				slog.Error("job run failed", "job", r.Name, "err", err)
			}
		}
	}
}

func (r Runner) runOnce(ctx context.Context) error {
	start := time.Now()
	err := r.Run(ctx)
	slog.Info("job sweep finished", "job", r.Name, "elapsed", time.Since(start), "err", err)
	return err
}

// Leadership holds a PostgreSQL advisory lock on a dedicated connection.
// The connection stays acquired for the whole hold: never return it to
// the pool while leading, or another borrower inherits the lock.
type Leadership struct {
	conn *pgxpool.Conn
	key  int64
}

// AcquireLeadership tries a non-blocking advisory lock. ok=false means a
// peer leads; the caller should skip this tick, not error.
func AcquireLeadership(ctx context.Context, pool *pgxpool.Pool, key int64) (*Leadership, bool, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("acquire conn for leadership: %w", err)
	}
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&locked); err != nil {
		conn.Release()
		return nil, false, fmt.Errorf("try advisory lock: %w", err)
	}
	if !locked {
		conn.Release()
		return nil, false, nil
	}
	return &Leadership{conn: conn, key: key}, true, nil
}

// Release unlocks and returns the connection to the pool.
func (l *Leadership) Release(ctx context.Context) {
	if _, err := l.conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, l.key); err != nil {
		slog.Warn("leadership unlock failed", "err", err)
	}
	l.conn.Release()
}

// InShard reports whether tenant belongs to this integrity shard.
// Sharding is stable (FNV-1a) so replicas never double-verify a tenant.
func InShard(tenant string, index, count int) bool {
	if count <= 1 {
		return true
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(tenant))
	return int(h.Sum32()%uint32(count)) == index%count
}

// Advisory-lock keys make single-leader jobs mutually exclusive across
// replicas. Keys are arbitrary but must be stable and unique per job.
const (
	DispositionLockKey int64 = 0xC0DE0001
	SweeperLockKey     int64 = 0xC0DE0002
)
