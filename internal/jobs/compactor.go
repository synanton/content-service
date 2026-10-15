package jobs

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/synanton/content-service/internal/adapter/parquet"
	"github.com/synanton/content-service/internal/observability"
	"github.com/synanton/content-service/internal/port"
)

// Compactor merges small immutable Parquet files into bigger ones.
//
// Every crash window heals via the orphan sweeper: a merged file written
// but never referenced is an unlisted object past grace; old files whose
// rows were repointed are unreferenced too. The merge order is fixed
// (oldest first), so row offsets stay exact without per-row bookkeeping.
type Compactor struct {
	Store      LifecycleStore
	Blobs      port.BlobStore
	TargetRows int
	MaxFiles   int
	Batch      int
}

func (c *Compactor) Sweep(ctx context.Context) error {
	tenants, err := c.Store.ListTenants(ctx)
	if err != nil {
		return fmt.Errorf("compactor: list tenants: %w", err)
	}
	for _, tenant := range tenants {
		if err := c.compactTenant(ctx, tenant); err != nil {
			return err
		}
	}
	candidates, err := c.Store.ListParquetCandidates(ctx, c.Batch)
	if err != nil {
		return fmt.Errorf("compactor: list candidates: %w", err)
	}
	counts := map[string]int{}
	for _, cand := range candidates {
		counts[cand.TenantID]++
	}
	for tenant, n := range counts {
		observability.ParquetCandidates.WithLabelValues(tenant).Set(float64(n))
	}
	return nil
}

func (c *Compactor) compactTenant(ctx context.Context, tenant string) error {
	files, err := c.Store.ListSmallParquetFiles(ctx, tenant, c.TargetRows, c.MaxFiles)
	if err != nil {
		return fmt.Errorf("compactor: list small files for %s: %w", tenant, err)
	}
	if len(files) < 2 {
		return nil
	}
	var rows []parquet.Row
	for _, f := range files {
		data, err := c.Blobs.Get(ctx, f.Key)
		if err != nil {
			return fmt.Errorf("compactor: read %s: %w", f.Key, err)
		}
		decoded, err := parquet.DecodeAll(data)
		if err != nil {
			return fmt.Errorf("compactor: decode %s: %w", f.Key, err)
		}
		rows = append(rows, decoded...)
	}
	encoded, err := parquet.EncodeAll(rows, "snappy")
	if err != nil {
		return err
	}
	newID := uuid.NewString()
	newKey := "parquet/" + tenant + "/" + newID + ".parquet"
	if err := c.Blobs.Put(ctx, newKey, encoded, "application/x-parquet"); err != nil {
		return fmt.Errorf("compactor: write %s: %w", newKey, err)
	}
	if err := c.Store.InsertParquetFile(ctx, ParquetFile{
		FileID: newID, Tenant: tenant, Key: newKey, Rows: len(rows), Bytes: int64(len(encoded)),
	}); err != nil {
		return err
	}
	var offset int64
	for _, f := range files {
		if err := c.Store.SwapParquetFilePointers(ctx, tenant, f.FileID, newID, offset); err != nil {
			return err
		}
		offset += int64(f.Rows)
	}
	for _, f := range files {
		if err := c.Store.DeleteParquetFile(ctx, f.FileID); err != nil {
			return err
		}
		if err := c.Blobs.Delete(ctx, f.Key); err != nil {
			slog.Warn("compactor old object cleanup failed (sweeper heals)", "key", f.Key, "err", err)
		}
		observability.ParquetMergedFiles.WithLabelValues(tenant).Inc()
	}
	observability.ParquetMergedRows.WithLabelValues(tenant).Add(float64(len(rows)))
	slog.Info("compactor merged files", "tenant", tenant, "files", len(files), "rows", len(rows), "into", newKey)
	return nil
}
