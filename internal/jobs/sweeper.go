package jobs

import (
	"context"
	"fmt"
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
		tenant, hex, ok := splitKey(obj.Key)
		if !ok {
			logf("sweeper: skipping foreign key shape %q", obj.Key)
			continue
		}
		exists, err := s.Store.ArtifactExistsByDigest(ctx, tenant, "sha256:"+hex)
		if err != nil {
			return fmt.Errorf("sweeper: artifact lookup %s: %w", obj.Key, err)
		}
		if exists || obj.ModifiedAt.After(cutoff) {
			continue
		}
		counts[tenant]++
		if s.DryRun {
			logf("sweeper: dry-run would delete orphan %s (modified %v)", obj.Key, obj.ModifiedAt)
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
