//go:build integration

package integration

import (
	"bytes"
	"testing"

	"github.com/synanton/content-service/internal/jobs"
	"github.com/synanton/content-service/internal/port"
	"github.com/synanton/content-service/pkg/digest"
)

func fileRowCount(t *testing.T, tenantID string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM parquet_files WHERE tenant_id = $1`, tenantID,
	).Scan(&n); err != nil {
		t.Fatalf("file row count: %v", err)
	}
	return n
}

func artifactFileKey(t *testing.T, tenantID, artifactID string) string {
	t.Helper()
	var key *string
	if err := pool.QueryRow(ctx,
		`SELECT f.s3_key FROM artifacts a LEFT JOIN parquet_files f ON f.file_id = a.parquet_file_id
		 WHERE a.tenant_id = $1 AND a.artifact_id = $2::uuid`,
		tenantID, artifactID,
	).Scan(&key); err != nil {
		t.Fatalf("artifact file key: %v", err)
	}
	if key == nil {
		return ""
	}
	return *key
}

func TestParquetTierWritesRealFile(t *testing.T) {
	tn := tenant()
	payload := largePayload('p', 5000)
	res, err := store.Put(ctx, port.PutParams{
		TenantID: tn, Payload: payload, ContentType: "application/octet-stream",
		OwnerID: "owner-1", RetentionDays: 30,
	})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if res.Artifact.StorageTier != "parquet" {
		t.Fatalf("tier = %s, want parquet", res.Artifact.StorageTier)
	}
	if fileRowCount(t, tn) != 1 {
		t.Fatal("parquet put must register exactly one file row")
	}
	key := artifactFileKey(t, tn, res.Artifact.ArtifactID)
	if key == "" {
		t.Fatal("artifact row must point at its parquet file")
	}
	if ok, err := blobs.Exists(ctx, key); err != nil || !ok {
		t.Fatalf("parquet file object missing: %v, %v", ok, err)
	}
	data, _, err := store.Get(ctx, tn, res.Artifact.ArtifactID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !bytes.Equal(data, payload) {
		t.Fatal("payload mismatch after parquet row read")
	}
}

func TestLegacyParquetRowFallsBack(t *testing.T) {
	tn := tenant()
	payload := largePayload('q', 5000)
	dgst := digest.Compute(payload)
	artifactID := "11111111-1111-1111-1111-111111111111"
	if _, err := pool.Exec(ctx,
		`INSERT INTO artifacts
			(tenant_id, artifact_id, digest, content_type, size_bytes, storage_tier, expires_at)
		 VALUES ($1, $2::uuid, $3, 'application/octet-stream', $4, 'parquet', now() + interval '30 days')`,
		tn, artifactID, dgst, len(payload),
	); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}
	if err := blobs.Put(ctx, tn+"/"+dgst[len("sha256:"):], payload, "application/octet-stream"); err != nil {
		t.Fatalf("seed legacy object: %v", err)
	}
	data, _, err := store.Get(ctx, tn, artifactID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !bytes.Equal(data, payload) {
		t.Fatal("legacy parquet row must read via the per-artifact object fallback")
	}
}

func TestCompactorMergesSmallFiles(t *testing.T) {
	tn := tenant()
	payloads := map[string][]byte{}
	for i := 0; i < 5; i++ {
		payload := largePayload(byte('r'+i), 5000+i*100)
		res, err := store.Put(ctx, port.PutParams{
			TenantID: tn, Payload: payload, OwnerID: "owner-1", RetentionDays: 30,
		})
		if err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
		payloads[res.Artifact.ArtifactID] = payload
	}
	if fileRowCount(t, tn) != 5 {
		t.Fatalf("expected 5 single-row files before compaction")
	}
	var oldKeys []string
	files, err := store.ListSmallParquetFiles(ctx, tn, 1000, 32)
	if err != nil {
		t.Fatalf("list small files: %v", err)
	}
	for _, f := range files {
		oldKeys = append(oldKeys, f.Key)
	}
	compactor := &jobs.Compactor{Store: store, Blobs: blobs, TargetRows: 1000, MaxFiles: 32, Batch: 10000}
	if err := compactor.Sweep(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if fileRowCount(t, tn) != 1 {
		t.Fatalf("expected 1 merged file after compaction, got %d", fileRowCount(t, tn))
	}
	for artifactID, payload := range payloads {
		data, _, err := store.Get(ctx, tn, artifactID)
		if err != nil {
			t.Fatalf("get %s: %v", artifactID, err)
		}
		if !bytes.Equal(data, payload) {
			t.Fatalf("payload mismatch for %s after merge", artifactID)
		}
	}
	for _, key := range oldKeys {
		if ok, err := blobs.Exists(ctx, key); err != nil || ok {
			t.Fatalf("old file object %s must be gone: %v, %v", key, ok, err)
		}
	}
}

func TestCompactorSkipsSingleFile(t *testing.T) {
	tn := tenant()
	res, err := store.Put(ctx, port.PutParams{
		TenantID: tn, Payload: largePayload('s', 5000), OwnerID: "owner-1", RetentionDays: 30,
	})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	compactor := &jobs.Compactor{Store: store, Blobs: blobs, TargetRows: 1000, MaxFiles: 32, Batch: 10000}
	if err := compactor.Sweep(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if fileRowCount(t, tn) != 1 {
		t.Fatal("a lone file must not be pointlessly re-merged")
	}
	if _, _, err := store.Get(ctx, tn, res.Artifact.ArtifactID); err != nil {
		t.Fatalf("get: %v", err)
	}
}

func TestSweeperCollectsOrphanParquetFile(t *testing.T) {
	tn := tenant()
	ghost := "parquet/" + tn + "/ghost.parquet"
	if err := blobs.Put(ctx, ghost, []byte("unlisted bytes"), "application/x-parquet"); err != nil {
		t.Fatalf("seed ghost: %v", err)
	}
	live := mustPutParquet(t, tn)
	liveKey := artifactFileKey(t, tn, live.Artifact.ArtifactID)
	sweep := &jobs.Sweeper{Store: store, Blobs: blobs, Batch: 10000}
	if err := sweep.Sweep(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if ok, err := blobs.Exists(ctx, ghost); err != nil || ok {
		t.Fatalf("unlisted parquet file must be collected: %v, %v", ok, err)
	}
	if ok, err := blobs.Exists(ctx, liveKey); err != nil || !ok {
		t.Fatalf("listed parquet file must survive: %v, %v", ok, err)
	}
}

func mustPutParquet(t *testing.T, tenantID string) *port.PutResult {
	t.Helper()
	res, err := store.Put(ctx, port.PutParams{
		TenantID: tenantID, Payload: largePayload('t', 5000),
		ContentType: "application/octet-stream", OwnerID: "owner-1", RetentionDays: 30,
	})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if res.Artifact.StorageTier != "parquet" {
		t.Fatalf("tier = %s, want parquet", res.Artifact.StorageTier)
	}
	return res
}

func TestDispositionCleansParquetFile(t *testing.T) {
	tn := tenant()
	res := mustPutParquet(t, tn)
	key := artifactFileKey(t, tn, res.Artifact.ArtifactID)
	expireRetention(t, tn, res.Artifact.ArtifactID)
	if err := store.MarkForDeletion(ctx, tn, res.Artifact.ArtifactID, "owner-1"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := newDisposition().Sweep(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if ok, err := store.Exists(ctx, tn, res.Artifact.ArtifactID); err != nil || ok {
		t.Fatalf("disposed artifact still exists: %v, %v", ok, err)
	}
	if fileRowCount(t, tn) != 0 {
		t.Fatal("orphaned file row must be reaped with its last artifact")
	}
	if ok, err := blobs.Exists(ctx, key); err != nil || ok {
		t.Fatalf("disposed file object still exists: %v, %v", ok, err)
	}
}
