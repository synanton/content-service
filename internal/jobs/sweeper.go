package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/synanton/content-service/internal/observability"
	"github.com/synanton/content-service/internal/port"
)

// Sweeper reconciles metadata against object storage in both directions.
//
//   - Metadata without payload: an external-tier artifact row whose object
//     is gone is alerted and audited, never auto-deleted (deleting metadata
//     on a storage blip would destroy the only record of what was lost).
//   - Payload without metadata: an object with no referencing artifact older
//     than the grace period is deleted (or only logged in dry-run mode).
//
// Object keys outside the <tenant>/<hex> shape are skipped with a log line:
// the bucket is dedicated to this service, but deleting foreign data on a
// naming guess is never acceptable.
type Sweeper struct {
	Store   LifecycleStore
	Blobs   port.BlobStore
	Grace   time.Duration
	Batch   int
	DryRun  bool
}

func (s *Sweeper) Sweep(ctx context.Context) error {
	lister, ok := s.Blobs.(port.BlobLister)
	if !ok {
		return fmt.Errorf("sweeper: blob store cannot list objects")
	}
	if err := s.sweepMissingBlobs(ctx); err != nil {
		return err
	}
	return s.sweepOrphanBlobs(ctx, lister)
}

func (s *Sweeper) sweepMissingBlobs(ctx context.Context) error {
	externals, err := s.Store.ListExternalArtifacts(ctx, s.Batch)
	if err != nil {
		return fmt.Errorf("sweeper: list external artifacts: %w", err)
	}
	missing := map[string]int{}
	for _, a := range externals {
		ok, err := s.Blobs.Exists(ctx, blobObjectKey(a.TenantID, a.Digest))
		if err != nil {
			return fmt.Errorf("sweeper: exists %s/%s: %w", a.TenantID, a.ArtifactID, err)
		}
		if !ok {
			missing[a.TenantID]++
			if err := s.Store.RecordIntegrityFailure(ctx, a.TenantID, a.ArtifactID,
				"orphan metadata: object missing in storage tier "+a.StorageTier); err != nil {
				return fmt.Errorf("sweeper: record missing blob %s/%s: %w",
					a.TenantID, a.ArtifactID, err)
			}
			observability.IntegrityFailures.WithLabelValues(a.TenantID).Inc()
		}
	}
	for tenant, n := range missing {
		observability.OrphanCandidates.WithLabelValues(tenant, "metadata").Set(float64(n))
	}
	return nil
}

func (s *Sweeper) sweepOrphanBlobs(ctx context.Context, lister port.BlobLister) error {
	objects, err := lister.List(ctx, "")
	if err != nil {
		return fmt.Errorf("sweeper: list objects: %w", err)
	}
	cutoff := time.Now().UTC().Add(-s.Grace)
	counts := map[string]int{}
	for _, obj := range objects {
		live, tenant, err := s.referenced(ctx, obj.Key)
		if err != nil {
			return err
		}
		if live || obj.ModifiedAt.After(cutoff) {
			continue
		}
		if tenant == "" {
			slog.Info("sweeper skipping foreign key shape", "key", obj.Key)
			continue
		}
		counts[tenant]++
		if s.DryRun {
			slog.Info("sweeper dry-run would delete orphan", "key", obj.Key, "modified", obj.ModifiedAt)
			continue
		}
		if err := s.Blobs.Delete(ctx, obj.Key); err != nil {
			return fmt.Errorf("sweeper: delete orphan %s: %w", obj.Key, err)
		}
		observability.OrphanDeleted.WithLabelValues(tenant).Inc()
	}
	for tenant, n := range counts {
		observability.OrphanCandidates.WithLabelValues(tenant, "blob").Set(float64(n))
	}
	return nil
}

// referenced reports whether an object key is live and, when the key shape
// is recognized, which tenant it belongs to. Unknown shapes return
// ("", tenant="", nil) so callers skip rather than delete foreign data.
func (s *Sweeper) referenced(ctx context.Context, key string) (live bool, tenant string, err error) {
	if fileTenant, file, ok := splitParquetKey(key); ok {
		live, err := s.Store.ParquetFileKeyExists(ctx, key)
		if err != nil {
			return false, "", fmt.Errorf("sweeper: parquet lookup %s: %w", key, err)
		}
		_ = file
		return live, fileTenant, nil
	}
	tenant, hex, ok := splitKey(key)
	if !ok {
		return false, "", nil
	}
	live, err = s.Store.ArtifactExistsByDigest(ctx, tenant, "sha256:"+hex)
	if err != nil {
		return false, "", fmt.Errorf("sweeper: artifact lookup %s: %w", key, err)
	}
	return live, tenant, nil
}

func splitParquetKey(key string) (tenant, file string, ok bool) {
	tenant, rest, found := strings.Cut(key, "/")
	if !found || tenant != "parquet" {
		return "", "", false
	}
	owner, name, found := strings.Cut(rest, "/")
	if !found || owner == "" || !strings.HasSuffix(name, ".parquet") || strings.Contains(name, "/") {
		return "", "", false
	}
	return owner, name, true
}

func splitKey(key string) (tenant, hex string, ok bool) {
	tenant, hex, found := strings.Cut(key, "/")
	if !found || tenant == "" || hex == "" || strings.Contains(hex, "/") {
		return "", "", false
	}
	for _, c := range hex {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return "", "", false
		}
	}
	return tenant, hex, true
}
