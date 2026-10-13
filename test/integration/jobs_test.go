//go:build integration

package integration

import (
	"testing"
	"time"

	adapterpg "github.com/synanton/content-service/internal/adapter/postgres"
	"github.com/synanton/content-service/internal/jobs"
	"github.com/synanton/content-service/internal/port"
)

func expireRetention(t *testing.T, tenantID, artifactID string) {
	t.Helper()
	if _, err := pool.Exec(ctx,
		`UPDATE retention_policies SET expires_at = now() - interval '2 hours'
		 WHERE tenant_id = $1 AND artifact_id = $2::uuid`,
		tenantID, artifactID,
	); err != nil {
		t.Fatalf("expire retention: %v", err)
	}
}

func auditCount(t *testing.T, tenantID, action string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM audit_log WHERE tenant_id = $1 AND action = $2`,
		tenantID, action,
	).Scan(&n); err != nil {
		t.Fatalf("audit count: %v", err)
	}
	return n
}

func newDisposition() *jobs.Disposition {
	return &jobs.Disposition{Store: store, Blobs: blobs, Grace: time.Hour, Batch: 100}
}

func TestDispositionDeletesExpiredAndReleased(t *testing.T) {
	tn := tenant()
	res := mustPut(t, tn, []byte("disposition candidate"))
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
	if auditCount(t, tn, "disposition_delete") != 1 {
		t.Fatal("disposition must audit the deletion exactly once")
	}
}

func TestDispositionKeepsLegalHold(t *testing.T) {
	tn := tenant()
	res := mustPut(t, tn, []byte("held artifact"))
	expireRetention(t, tn, res.Artifact.ArtifactID)
	if err := store.SetLegalHold(ctx, tn, res.Artifact.ArtifactID, true, "admin-1", "hold"); err != nil {
		t.Fatalf("hold: %v", err)
	}
	if err := store.MarkForDeletion(ctx, tn, res.Artifact.ArtifactID, "owner-1"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := newDisposition().Sweep(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if ok, err := store.Exists(ctx, tn, res.Artifact.ArtifactID); err != nil || !ok {
		t.Fatalf("held artifact must survive disposition: %v, %v", ok, err)
	}
}

func TestDispositionKeepsLiveReferences(t *testing.T) {
	tn := tenant()
	res := mustPut(t, tn, []byte("live artifact"))
	expireRetention(t, tn, res.Artifact.ArtifactID)
	if err := newDisposition().Sweep(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if ok, err := store.Exists(ctx, tn, res.Artifact.ArtifactID); err != nil || !ok {
		t.Fatalf("referenced artifact must survive disposition: %v, %v", ok, err)
	}
}

func TestDispositionDeletesObjectTierAndBlob(t *testing.T) {
	tn := tenant()
	res, err := store.Put(ctx, port.PutParams{
		TenantID: tn, Payload: largePayload('z', 300*1024), OwnerID: "owner-1", RetentionDays: 30,
	})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
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
	if ok, err := blobs.Exists(ctx, adapterpg.BlobKey(tn, res.Artifact.Digest)); err != nil || ok {
		t.Fatalf("disposed blob still exists: %v, %v", ok, err)
	}
}

func TestIntegritySweepHealthy(t *testing.T) {
	tn := tenant()
	mustPut(t, tn, []byte("healthy one"))
	mustPut(t, tn, largePayload('y', 300*1024))
	sweep := &jobs.Integrity{Store: store, Content: store, Batch: 100, SampleRate: 1}
	result, err := sweep.Sweep(ctx)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if result.Checked < 2 {
		t.Fatalf("checked = %d, want at least 2", result.Checked)
	}
	if result.Failed != 0 {
		t.Fatalf("failed = %d, want 0 on healthy data", result.Failed)
	}
}

func TestIntegritySweepFindsTamperedPayload(t *testing.T) {
	tn := tenant()
	res := mustPut(t, tn, []byte("will be tampered"))
	if _, err := pool.Exec(ctx,
		`UPDATE artifacts SET payload = $3 WHERE tenant_id = $1 AND artifact_id = $2::uuid`,
		tn, res.Artifact.ArtifactID, []byte("tampered bytes here")); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	sweep := &jobs.Integrity{
		Store: store, Content: store, Batch: 10000, SampleRate: 1,
		ShardIndex: 0, ShardCount: 1,
	}
	before := auditCount(t, tn, "integrity_mismatch")
	result, err := sweep.Sweep(ctx)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if result.Failed < 1 {
		t.Fatal("tampered payload must be reported as failed")
	}
	if auditCount(t, tn, "integrity_mismatch") <= before {
		t.Fatal("integrity failure must be audited")
	}
}

func TestSweeperReportsMissingBlob(t *testing.T) {
	tn := tenant()
	res, err := store.Put(ctx, port.PutParams{
		TenantID: tn, Payload: largePayload('w', 300*1024), OwnerID: "owner-1", RetentionDays: 30,
	})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := blobs.Delete(ctx, adapterpg.BlobKey(tn, res.Artifact.Digest)); err != nil {
		t.Fatalf("delete object: %v", err)
	}
	sweep := &jobs.Sweeper{Store: store, Blobs: blobs, Batch: 10000}
	if err := sweep.Sweep(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if auditCount(t, tn, "integrity_mismatch") < 1 {
		t.Fatal("metadata without payload must be audited")
	}
	if ok, err := store.Exists(ctx, tn, res.Artifact.ArtifactID); err != nil || !ok {
		t.Fatalf("metadata row must not be auto-deleted: %v, %v", ok, err)
	}
}

func TestSweeperDeletesOrphanBlob(t *testing.T) {
	tn := tenant()
	key := tn + "/ab12cd34ef56ab12cd34ef56ab12cd34ef56ab12cd34ef56ab12cd34ef56ab12"
	if err := blobs.Put(ctx, key, []byte("orphan bytes"), "application/octet-stream"); err != nil {
		t.Fatalf("seed orphan: %v", err)
	}
	sweep := &jobs.Sweeper{Store: store, Blobs: blobs, Batch: 10000}
	if err := sweep.Sweep(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if ok, err := blobs.Exists(ctx, key); err != nil || ok {
		t.Fatalf("orphan blob must be deleted: %v, %v", ok, err)
	}
}

func TestSweeperDryRunKeepsOrphanBlob(t *testing.T) {
	tn := tenant()
	key := tn + "/cd34ef56ab12cd34ef56ab12cd34ef56ab12cd34ef56ab12cd34ef56ab12cd34"
	if err := blobs.Put(ctx, key, []byte("orphan bytes"), "application/octet-stream"); err != nil {
		t.Fatalf("seed orphan: %v", err)
	}
	sweep := &jobs.Sweeper{Store: store, Blobs: blobs, Batch: 10000, DryRun: true}
	if err := sweep.Sweep(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if ok, err := blobs.Exists(ctx, key); err != nil || !ok {
		t.Fatalf("dry run must not delete: %v, %v", ok, err)
	}
	if err := blobs.Delete(ctx, key); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
}

func TestCompactorIsNonDestructive(t *testing.T) {
	custom := *cfg
	custom.Storage.InlineThresholdBytes = 100
	custom.Storage.ObjectThresholdBytes = 100000
	parquetStore := adapterpg.NewStore(pool, &custom, blobs)
	tn := tenant()
	res, err := parquetStore.Put(ctx, port.PutParams{
		TenantID: tn, Payload: largePayload('v', 5000), OwnerID: "owner-1", RetentionDays: 30,
	})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := (&jobs.Compactor{Store: store, Batch: 10000}).Sweep(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if ok, err := store.Exists(ctx, tn, res.Artifact.ArtifactID); err != nil || !ok {
		t.Fatalf("compactor must not delete: %v, %v", ok, err)
	}
	data, _, err := store.Get(ctx, tn, res.Artifact.ArtifactID)
	if err != nil {
		t.Fatalf("get after compact: %v", err)
	}
	if len(data) != 5000 {
		t.Fatalf("payload size = %d, want 5000", len(data))
	}
}

func TestLeadershipMutex(t *testing.T) {
	first, ok, err := jobs.AcquireLeadership(ctx, pool, 0xC0DEEEEE)
	if err != nil || !ok {
		t.Fatalf("first acquire ok = %v, err = %v; want true, nil", ok, err)
	}
	_, ok, err = jobs.AcquireLeadership(ctx, pool, 0xC0DEEEEE)
	if err != nil || ok {
		t.Fatalf("second acquire ok = %v, err = %v; want false, nil", ok, err)
	}
	first.Release(ctx)
	third, ok, err := jobs.AcquireLeadership(ctx, pool, 0xC0DEEEEE)
	if err != nil || !ok {
		t.Fatalf("reacquire ok = %v, err = %v; want true, nil", ok, err)
	}
	third.Release(ctx)
}
