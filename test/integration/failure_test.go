//go:build integration

package integration

import (
	"fmt"
	"sync"
	"testing"

	adapterpg "github.com/synanton/content-service/internal/adapter/postgres"
	s3adapter "github.com/synanton/content-service/internal/adapter/s3"
	"github.com/synanton/content-service/internal/jobs"
	"github.com/synanton/content-service/internal/port"
)

// deadObjectStore returns a BlobStore pointed at a closed port: every
// operation fails fast with connection refused, modeling a hard object
// storage outage without fault-injection machinery.
func deadObjectStore(t *testing.T) port.BlobStore {
	t.Helper()
	dead, err := s3adapter.NewStore(s3adapter.Config{
		Endpoint: "127.0.0.1:1",
		Bucket:   "content-it",
		Region:   "us-east-1",
	})
	if err != nil {
		t.Fatalf("dead store: %v", err)
	}
	return dead
}

// TestObjectStoreOutageDegradesGracefully pins the failure boundary: object
// tiers fail fast with errors while inline traffic keeps working, and reads
// of already-externalized payloads fail closed (never empty bytes, never panic).
func TestObjectStoreOutageDegradesGracefully(t *testing.T) {
	outage := adapterpg.NewStore(pool, cfg, deadObjectStore(t), nil)

	if _, err := outage.Put(ctx, port.PutParams{
		TenantID: tenant(), Payload: largePayload('o', 300*1024), OwnerID: "owner-1",
	}); err == nil {
		t.Fatal("object-tier put during an outage must fail fast")
	}

	tn := tenant()
	small := []byte("inline still serves during an outage")
	res, err := outage.Put(ctx, port.PutParams{
		TenantID: tn, Payload: small, OwnerID: "owner-1", RetentionDays: 30,
	})
	if err != nil {
		t.Fatalf("inline put during an outage: %v", err)
	}
	data, _, err := outage.Get(ctx, tn, res.Artifact.ArtifactID)
	if err != nil {
		t.Fatalf("inline get during an outage: %v", err)
	}
	if string(data) != string(small) {
		t.Fatal("inline bytes must survive an object-storage outage")
	}

	bigTenant := tenant()
	big, err := store.Put(ctx, port.PutParams{
		TenantID: bigTenant, Payload: largePayload('p', 300*1024),
		OwnerID: "owner-1", RetentionDays: 30,
	})
	if err != nil {
		t.Fatalf("seed large artifact: %v", err)
	}
	if _, _, err := outage.Get(ctx, bigTenant, big.Artifact.ArtifactID); err == nil {
		t.Fatal("get of an externalized payload during an outage must fail closed")
	}
}

// TestConcurrentReleaseAndGet proves reference release never disturbs reads:
// Get ignores the reference registry by design (release is not deletion),
// so releasers and readers may overlap arbitrarily.
func TestConcurrentReleaseAndGet(t *testing.T) {
	tn := tenant()
	res := mustPut(t, tn, []byte("release/get overlap probe"))
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			for j := 0; j < 25; j++ {
				if i%2 == 0 {
					if err := store.MarkForDeletion(ctx, tn, res.Artifact.ArtifactID, "owner-1"); err != nil {
						errs[i] = err
						return
					}
				} else {
					data, _, err := store.Get(ctx, tn, res.Artifact.ArtifactID)
					if err != nil {
						errs[i] = err
						return
					}
					if string(data) != "release/get overlap probe" {
						errs[i] = fmt.Errorf("bytes changed under release")
						return
					}
				}
			}
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}
}

// TestDispositionRerunIsNoOp proves sweeps are safe to repeat: crash recovery
// simply re-runs the sweep, and a second run must change nothing and fail nothing.
func TestDispositionRerunIsNoOp(t *testing.T) {
	tn := tenant()
	res := mustPut(t, tn, []byte("disposition rerun probe"))
	expireRetention(t, tn, res.Artifact.ArtifactID)
	if err := store.MarkForDeletion(ctx, tn, res.Artifact.ArtifactID, "owner-1"); err != nil {
		t.Fatalf("release: %v", err)
	}
	sweep := newDisposition()
	if err := sweep.Sweep(ctx); err != nil {
		t.Fatalf("first sweep: %v", err)
	}
	if err := sweep.Sweep(ctx); err != nil {
		t.Fatalf("second sweep must be a safe no-op: %v", err)
	}
	if auditCount(t, tn, "disposition_delete") != 1 {
		t.Fatal("re-runs must not double-audit the deletion")
	}
}

// TestCompactorRerunIsNoOp proves a re-run after a completed merge changes
// nothing: leftover old files were already deleted, the merged file is above
// the small-file threshold for its size class in this test only when no
// second merge triggers.
func TestCompactorRerunIsNoOp(t *testing.T) {
	tn := tenant()
	for i := 0; i < 3; i++ {
		if _, err := store.Put(ctx, port.PutParams{
			TenantID: tn, Payload: largePayload(byte('q'+i), 5000),
			OwnerID: "owner-1", RetentionDays: 30,
		}); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}
	compactor := &jobs.Compactor{Store: store, Blobs: blobs, TargetRows: 1000, MaxFiles: 32, Batch: 10000}
	if err := compactor.Sweep(ctx); err != nil {
		t.Fatalf("first sweep: %v", err)
	}
	before := fileRowCount(t, tn)
	if err := compactor.Sweep(ctx); err != nil {
		t.Fatalf("second sweep must be safe: %v", err)
	}
	if after := fileRowCount(t, tn); after != before {
		t.Fatalf("file rows changed on re-run: %d -> %d", before, after)
	}
}
