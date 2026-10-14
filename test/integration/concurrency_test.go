//go:build integration

package integration

import (
	"fmt"
	"sync"
	"testing"

	"github.com/synanton/content-service/internal/port"
)

// TestConcurrentDedupSingleArtifact races N simultaneous puts of identical
// bytes: content-addressability requires exactly one artifact row, all puts
// succeed, and all but the winner report a dedup hit. A loser must take the
// winner's reference, never a foreign key violation or a dangling id.
func TestConcurrentDedupSingleArtifact(t *testing.T) {
	testConcurrentDedup(t, []byte("concurrent dedup probe payload"), 32)
}

func TestConcurrentDedupObjectTier(t *testing.T) {
	testConcurrentDedup(t, largePayload('h', 300*1024), 8)
}

func TestConcurrentDedupParquetTier(t *testing.T) {
	testConcurrentDedup(t, largePayload('i', 5000), 8)
}

func testConcurrentDedup(t *testing.T, data []byte, n int) {
	t.Helper()
	tn := tenant()
	start := make(chan struct{})
	results := make([]*port.PutResult, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			res, err := store.Put(ctx, port.PutParams{
				TenantID:      tn,
				Payload:       data,
				ContentType:   "application/octet-stream",
				OwnerID:       fmt.Sprintf("owner-%d", i),
				RetentionDays: 30,
			})
			results[i], errs[i] = res, err
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: put: %v", i, err)
		}
	}
	ids := map[string]int{}
	hits := 0
	for _, res := range results {
		ids[res.Artifact.ArtifactID]++
		if res.DedupHit {
			hits++
		}
	}
	if len(ids) != 1 {
		t.Fatalf("distinct artifact ids = %d, want exactly 1", len(ids))
	}
	if hits != n-1 {
		t.Fatalf("dedup hits = %d, want %d (every loser reports the winner)", hits, n-1)
	}
	var rows int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM artifacts WHERE tenant_id = $1`, tn,
	).Scan(&rows); err != nil {
		t.Fatalf("count artifacts: %v", err)
	}
	if rows != 1 {
		t.Fatalf("artifact rows = %d, want exactly 1", rows)
	}
}
