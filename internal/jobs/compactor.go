package jobs

import (
	"context"
	"fmt"

	"github.com/synanton/content-service/internal/observability"
)

// Compactor is the Phase 5 scaffold for Parquet batch compaction.
//
// Today the parquet tier shares the object backend (one object per payload),
// so there is nothing safe to merge yet: this job inventories parquet-tier
// artifacts, reports per-tenant counts and bytes, and takes no destructive
// action. Real compaction (row-group batching with parquet-go plus read-path
// support) is follow-up work; the gauge below is its demand signal.
type Compactor struct {
	Store LifecycleStore
	Batch int
}

func (c *Compactor) Sweep(ctx context.Context) error {
	candidates, err := c.Store.ListParquetCandidates(ctx, c.Batch)
	if err != nil {
		return fmt.Errorf("compactor: list candidates: %w", err)
	}
	counts := map[string]int{}
	var bytes int64
	for _, cand := range candidates {
		counts[cand.TenantID]++
		bytes += cand.SizeBytes
	}
	for tenant, n := range counts {
		observability.ParquetCandidates.WithLabelValues(tenant).Set(float64(n))
	}
	logf("compactor: %d parquet-tier artifacts (%d bytes) await compaction; no action taken",
		len(candidates), bytes)
	return nil
}
