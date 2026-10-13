package jobs

import (
	"context"
	"fmt"
	"time"

	"github.com/synanton/content-service/internal/observability"
	"github.com/synanton/content-service/internal/port"
)

// Disposition physically deletes expired artifacts whose references are all
// released and no legal hold applies (design §5.2, two-phase deletion).
//
// Deletion order is row-first, then blob: a crash between the two leaves an
// orphan blob, which the orphan sweeper heals after its grace period. The
// reverse order would leave a row pointing at missing bytes, which needs an
// operator. Blob cleanup failures are logged, never fatal to the sweep.
type Disposition struct {
	Store LifecycleStore
	Blobs port.BlobStore
	Grace time.Duration
	Batch int
}

func (d *Disposition) Sweep(ctx context.Context) error {
	start := time.Now()
	defer func() {
		observability.DispositionDuration.Observe(time.Since(start).Seconds())
	}()

	candidates, err := d.Store.ListDispositionCandidates(ctx, time.Now().UTC().Add(-d.Grace), d.Batch)
	if err != nil {
		return fmt.Errorf("disposition: list candidates: %w", err)
	}
	for _, c := range candidates {
		disposed, deleted, err := d.Store.DisposeArtifact(ctx, c.TenantID, c.ArtifactID, "system/disposition")
		if err != nil {
			return fmt.Errorf("disposition: dispose %s/%s: %w", c.TenantID, c.ArtifactID, err)
		}
		if !deleted {
			observability.DispositionSkipped.WithLabelValues(c.TenantID, "contention").Inc()
			continue
		}
		observability.DispositionDeleted.WithLabelValues(c.TenantID).Inc()
		if d.Blobs == nil {
			continue
		}
		key := ""
		switch {
		case disposed.FileKey != "":
			key = disposed.FileKey
		case disposed.Tier != "inline":
			key = blobObjectKey(c.TenantID, disposed.Digest)
		}
		if key != "" {
			if err := d.Blobs.Delete(ctx, key); err != nil {
				logf("disposition: blob cleanup %s/%s failed (orphan sweeper heals): %v",
					c.TenantID, c.ArtifactID, err)
			}
		}
	}
	return nil
}

func blobObjectKey(tenantID, digest string) string {
	hex, _, _ := cutDigest(digest)
	return tenantID + "/" + hex
}

func cutDigest(digest string) (string, string, bool) {
	for i := 0; i < len(digest); i++ {
		if digest[i] == ':' {
			return digest[i+1:], digest[:i], true
		}
	}
	return digest, "", false
}
