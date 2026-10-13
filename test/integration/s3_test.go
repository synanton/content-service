//go:build integration

package integration

import (
	"bytes"
	"testing"

	adapterpg "github.com/synanton/content-service/internal/adapter/postgres"
	"github.com/synanton/content-service/internal/port"
)

func largePayload(seed byte, size int) []byte {
	payload := make([]byte, size)
	for i := range payload {
		payload[i] = seed + byte(i%251)
	}
	return payload
}

func payloadIsNull(t *testing.T, tenantID, artifactID string) bool {
	t.Helper()
	var isNull bool
	err := pool.QueryRow(ctx,
		`SELECT payload IS NULL FROM artifacts WHERE tenant_id = $1 AND artifact_id = $2::uuid`,
		tenantID, artifactID,
	).Scan(&isNull)
	if err != nil {
		t.Fatalf("payload null check: %v", err)
	}
	return isNull
}

func TestObjectTierRoundTrip(t *testing.T) {
	tn := tenant()
	payload := largePayload('a', 300*1024)
	res, err := store.Put(ctx, port.PutParams{
		TenantID:      tn,
		Payload:       payload,
		ContentType:   "application/octet-stream",
		OwnerID:       "owner-1",
		RetentionDays: 30,
	})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if res.DedupHit {
		t.Fatal("first put must not be a dedup hit")
	}
	if res.Artifact.StorageTier != "s3" {
		t.Fatalf("tier = %s, want s3", res.Artifact.StorageTier)
	}
	if !payloadIsNull(t, tn, res.Artifact.ArtifactID) {
		t.Fatal("object-tier row must keep payload NULL in Postgres")
	}
	data, art, err := store.Get(ctx, tn, res.Artifact.ArtifactID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !bytes.Equal(data, payload) {
		t.Fatal("payload mismatch after S3 round trip")
	}
	if art.Digest != res.Artifact.Digest {
		t.Fatal("digest mismatch after S3 round trip")
	}
}

func TestObjectTierDedupSameTenant(t *testing.T) {
	tn := tenant()
	payload := largePayload('b', 300*1024)
	first, err := store.Put(ctx, port.PutParams{
		TenantID: tn, Payload: payload, OwnerID: "owner-1", RetentionDays: 30,
	})
	if err != nil {
		t.Fatalf("first put: %v", err)
	}
	second, err := store.Put(ctx, port.PutParams{
		TenantID: tn, Payload: payload, OwnerID: "owner-2", RetentionDays: 30,
	})
	if err != nil {
		t.Fatalf("second put: %v", err)
	}
	if !second.DedupHit {
		t.Fatal("second put of identical bytes must be a dedup hit")
	}
	if second.Artifact.ArtifactID != first.Artifact.ArtifactID {
		t.Fatal("dedup hit must return the same artifact id")
	}
}

func TestObjectTierCrossTenantIsolated(t *testing.T) {
	payload := largePayload('c', 300*1024)
	a, err := store.Put(ctx, port.PutParams{
		TenantID: tenant(), Payload: payload, OwnerID: "owner-1", RetentionDays: 30,
	})
	if err != nil {
		t.Fatalf("put tenant A: %v", err)
	}
	b, err := store.Put(ctx, port.PutParams{
		TenantID: tenant(), Payload: payload, OwnerID: "owner-1", RetentionDays: 30,
	})
	if err != nil {
		t.Fatalf("put tenant B: %v", err)
	}
	if a.Artifact.ArtifactID == b.Artifact.ArtifactID {
		t.Fatal("different tenants must get separate artifact references")
	}
	if b.DedupHit {
		t.Fatal("cross-tenant put must not report a dedup hit")
	}
}

func TestParquetTierUsesObjectStorage(t *testing.T) {
	custom := *cfg
	custom.Storage.InlineThresholdBytes = 100
	custom.Storage.ObjectThresholdBytes = 1000
	parquetStore := adapterpg.NewStore(pool, &custom, blobs, rows)

	tn := tenant()
	payload := largePayload('d', 500)
	res, err := parquetStore.Put(ctx, port.PutParams{
		TenantID: tn, Payload: payload, OwnerID: "owner-1", RetentionDays: 30,
	})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if res.Artifact.StorageTier != "parquet" {
		t.Fatalf("tier = %s, want parquet", res.Artifact.StorageTier)
	}
	if !payloadIsNull(t, tn, res.Artifact.ArtifactID) {
		t.Fatal("parquet-tier row must keep payload NULL in Postgres")
	}
	data, _, err := parquetStore.Get(ctx, tn, res.Artifact.ArtifactID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !bytes.Equal(data, payload) {
		t.Fatal("payload mismatch after parquet-tier round trip")
	}
}

func TestMissingObjectFailsClosed(t *testing.T) {
	tn := tenant()
	payload := largePayload('e', 300*1024)
	res, err := store.Put(ctx, port.PutParams{
		TenantID: tn, Payload: payload, OwnerID: "owner-1", RetentionDays: 30,
	})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE artifacts SET payload = NULL WHERE tenant_id = $1 AND artifact_id = $2::uuid`,
		tn, res.Artifact.ArtifactID); err != nil {
		t.Fatalf("force external: %v", err)
	}
	if err := blobs.Delete(ctx, adapterpg.BlobKey(tn, res.Artifact.Digest)); err != nil {
		t.Fatalf("delete object: %v", err)
	}
	if _, _, err := store.Get(ctx, tn, res.Artifact.ArtifactID); err == nil {
		t.Fatal("get of dangling reference must fail")
	}
}

func TestMissingBlobStoreRejectsObjectTier(t *testing.T) {
	bare := adapterpg.NewStore(pool, cfg, nil, nil)
	_, err := bare.Put(ctx, port.PutParams{
		TenantID: tenant(), Payload: largePayload('f', 300*1024),
		OwnerID: "owner-1", RetentionDays: 30,
	})
	if err == nil {
		t.Fatal("object-tier put without a blob store must fail")
	}
}
