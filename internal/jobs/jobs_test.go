package jobs

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestInShardCoversEveryTenantExactlyOnce(t *testing.T) {
	const shards = 4
	tenants := []string{"acme", "globex", "initech", "hooli", "acme&co", "x", "tenant-with-long-name-123"}
	for _, tenant := range tenants {
		hits := 0
		for i := 0; i < shards; i++ {
			if InShard(tenant, i, shards) {
				hits++
			}
		}
		if hits != 1 {
			t.Fatalf("tenant %q belongs to %d shards, want exactly 1", tenant, hits)
		}
	}
}

func TestInShardSingleReplicaVerifiesAll(t *testing.T) {
	if !InShard("any-tenant", 0, 1) {
		t.Fatal("single replica must verify every tenant")
	}
}

func TestRunnerExecutesImmediatelyThenStops(t *testing.T) {
	var runs atomic.Int32
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	Runner{Name: "test", Interval: time.Hour, Run: func(context.Context) error {
		runs.Add(1)
		return nil
	}}.Loop(ctx)
	if runs.Load() != 1 {
		t.Fatalf("runs = %d, want exactly 1 (immediate run, then ctx ends before first tick)", runs.Load())
	}
}

func TestRunnerSurvivesRunError(t *testing.T) {
	var runs atomic.Int32
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	Runner{Name: "test", Interval: 50*time.Millisecond, Run: func(context.Context) error {
		runs.Add(1)
		return context.DeadlineExceeded
	}}.Loop(ctx)
	if runs.Load() < 2 {
		t.Fatalf("runs = %d, want at least 2 (errors must not stop the loop)", runs.Load())
	}
}
