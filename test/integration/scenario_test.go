//go:build integration

// Workload scenarios: mixed-size traffic, throughput vs concurrency,
// sustained churn with retention lifecycle, and failure injection.
// These complement the micro-benchmarks in bench_test.go by exercising
// realistic distributions instead of single-size loops. See
// docs/benchmarks.md for recorded results and methodology.
package integration

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	adapterpg "github.com/synanton/content-service/internal/adapter/postgres"
	"github.com/synanton/content-service/internal/adapter/parquet"
	s3adapter "github.com/synanton/content-service/internal/adapter/s3"
	"github.com/synanton/content-service/internal/jobs"
	"github.com/synanton/content-service/internal/port"
)

// mixer executes one workload op against the shared store, tracking
// published artifacts for reads and dedup replays.
type mixer struct {
	gen *Generator
	rng *rand.Rand
	reg *Registry
	salt uint64
}

func newMixer(seed int64) *mixer {
	work := MixedWorkload(seed)
	return &mixer{
		gen: NewGenerator(work),
		rng: rand.New(rand.NewSource(seed + 1)),
		reg: NewRegistry(256),
	}
}

func (m *mixer) doOp() (string, time.Duration, error) {
	m.salt++
	tenant := m.gen.Tenant()
	if m.rng.Float64() < m.gen.work.ReadFraction {
		if id, ok := m.reg.SampleArtifact(tenant, m.rng); ok {
			start := time.Now()
			_, _, err := store.Get(ctx, tenant, id)
			return "get", time.Since(start), err
		}
	}
	band := m.gen.Band()
	var payload []byte
	if m.rng.Float64() < m.gen.work.DedupRatio {
		if p, ok := m.reg.SamplePayload(m.rng); ok && len(p) == band.Size {
			payload = p
		}
	}
	if payload == nil {
		payload = Payload(m.gen.work.Seed, band, m.salt)
	}
	start := time.Now()
	res, err := store.Put(ctx, port.PutParams{
		TenantID:      tenant,
		Payload:       payload,
		ContentType:   "application/octet-stream",
		OwnerID:       "load",
		RetentionDays: 30,
	})
	d := time.Since(start)
	if err != nil {
		return "put", d, err
	}
	m.reg.Record(tenant, res.Artifact.ArtifactID, payload)
	return "put", d, nil
}

// BenchmarkWorkloadMixed runs the default mixed scenario single-threaded:
// the latency baseline before concurrency enters the picture.
func BenchmarkWorkloadMixed(b *testing.B) {
	m := newMixer(42)
	var lat latencies
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, d, err := m.doOp()
		lat.add(d)
		if err != nil {
			b.Fatalf("op: %v", err)
		}
	}
	b.StopTimer()
	lat.report(b)
}

// BenchmarkThroughputVsConcurrency runs fixed work (400 mixed ops) at
// several worker counts and reports throughput. Closed-loop: workers
// issue the next op as soon as the previous completes.
func BenchmarkThroughputVsConcurrency(b *testing.B) {
	for _, workers := range []int{1, 4, 16, 64} {
		b.Run(fmt.Sprintf("workers=%d", workers), func(b *testing.B) {
			const totalOps = 400
			var mu sync.Mutex
			var lat latencies
			var errs int
			var wg sync.WaitGroup
			start := time.Now()
			for w := 0; w < workers; w++ {
				wg.Add(1)
				go func(w int) {
					defer wg.Done()
					m := newMixer(int64(1000 + w))
					ops := totalOps / workers
					if w == 0 {
						ops += totalOps % workers
					}
					for i := 0; i < ops; i++ {
						_, d, err := m.doOp()
						mu.Lock()
						lat.add(d)
						if err != nil {
							errs++
						}
						mu.Unlock()
					}
				}(w)
			}
			wg.Wait()
			elapsed := time.Since(start)
			if errs > 0 {
				b.Fatalf("%d op errors", errs)
			}
			b.ReportMetric(float64(totalOps)/elapsed.Seconds(), "ops/sec")
			b.Logf("workers=%d ops=%d elapsed=%v ops/sec=%.1f", workers, totalOps, elapsed,
				float64(totalOps)/elapsed.Seconds())
			lat.report(b)
		})
	}
}

// soakSeconds bounds the sustained churn run. Default 30s keeps CI-time
// sane; set CONTENT_BENCH_SOAK_SECONDS higher for overnight soaks.
func soakSeconds() int {
	if v := os.Getenv("CONTENT_BENCH_SOAK_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 30
}

// BenchmarkSustainedChurn runs mixed traffic against a deadline with
// periodic retention churn (expire + release + disposition sweep inline),
// exercising the lifecycle under load rather than in isolation.
func BenchmarkSustainedChurn(b *testing.B) {
	deadline := time.Now().Add(time.Duration(soakSeconds()) * time.Second)
	m := newMixer(7)
	var lat latencies
	var puts, gets, errs, sweeps int
	sweep := &jobs.Disposition{Store: store, Blobs: blobs, Grace: time.Hour, Batch: 10000}
	for i := 0; ; i++ {
		if time.Now().After(deadline) {
			break
		}
		op, d, err := m.doOp()
		lat.add(d)
		if err != nil {
			errs++
			continue
		}
		if op == "put" {
			puts++
		} else {
			gets++
		}
		if i > 0 && i%500 == 0 {
			tn := m.gen.Tenant()
			if _, err := pool.Exec(ctx,
				`UPDATE retention_policies SET expires_at = now() - interval '2 hours'
				 WHERE tenant_id = $1`, tn); err == nil {
				if err := sweep.Sweep(ctx); err == nil {
					sweeps++
				}
			}
		}
	}
	b.ReportMetric(float64(puts+gets), "ops")
	b.Logf("puts=%d gets=%d errs=%d sweeps=%d", puts, gets, errs, sweeps)
	if errs > 0 {
		b.Fatalf("%d op errors during soak", errs)
	}
	lat.report(b)
}

// slowBlobs injects latency into object-tier reads and writes to model a
// degraded (not dead) object store. The service has no server-side
// deadline today, so slow reads must still succeed with correct bytes;
// this test pins that behavior until deadlines land.
type slowBlobs struct {
	port.BlobStore
	delay time.Duration
}

func (s slowBlobs) sleep(ctx context.Context) error {
	timer := time.NewTimer(s.delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (s slowBlobs) Get(ctx context.Context, key string) ([]byte, error) {
	if err := s.sleep(ctx); err != nil {
		return nil, err
	}
	return s.BlobStore.Get(ctx, key)
}

func (s slowBlobs) Put(ctx context.Context, key string, data []byte, contentType string) error {
	if err := s.sleep(ctx); err != nil {
		return err
	}
	return s.BlobStore.Put(ctx, key, data, contentType)
}

func TestSlowObjectStoreStillServesCorrectBytes(t *testing.T) {
	tn := tenant()
	payload := largePayload('z', 300*1024)
	res, err := store.Put(ctx, port.PutParams{
		TenantID: tn, Payload: payload, OwnerID: "owner-1", RetentionDays: 30,
	})
	if err != nil {
		t.Fatalf("seed put: %v", err)
	}
	slow := adapterpg.NewStore(pool, cfg,
		slowBlobs{BlobStore: blobs, delay: 2 * time.Second}, rows)
	start := time.Now()
	data, _, err := slow.Get(ctx, tn, res.Artifact.ArtifactID)
	if err != nil {
		t.Fatalf("slow get: %v", err)
	}
	t.Logf("degraded read took %v", time.Since(start))
	if string(data) != string(payload) {
		t.Fatal("degraded read must return intact bytes")
	}
}

// TestMinioKillRecovery stops the object store mid-test: traffic fails,
// then the store restarts and previously persisted bytes read back intact.
// Detection is via errors (no health polling in the client); recovery is
// polled with a deadline.
func TestMinioKillRecovery(t *testing.T) {
	if minioContainer == nil {
		t.Skip("minio container handle unavailable")
	}
	tn := tenant()
	payload := largePayload('y', 300*1024)
	res, err := store.Put(ctx, port.PutParams{
		TenantID: tn, Payload: payload, OwnerID: "owner-1", RetentionDays: 30,
	})
	if err != nil {
		t.Fatalf("seed put: %v", err)
	}
	if err := minioContainer.Stop(ctx, nil); err != nil {
		t.Fatalf("stop minio: %v", err)
	}
	if _, err := store.Put(ctx, port.PutParams{
		TenantID: tn, Payload: largePayload('x', 300*1024), OwnerID: "owner-1",
	}); err == nil {
		t.Fatal("put during outage must fail")
	}
	if _, _, err := store.Get(ctx, tn, res.Artifact.ArtifactID); err == nil {
		t.Fatal("get during outage must fail")
	}
	if err := minioContainer.Start(ctx); err != nil {
		t.Fatalf("start minio: %v", err)
	}
	// Docker reallocates auto-assigned host ports across stop/start, so the
	// endpoint captured in TestMain is stale. Re-resolve and rebuild the
	// client, exactly as any reconnecting consumer must. Payload bytes live
	// on the container's volume and survive the restart.
	host, err := minioContainer.Host(ctx)
	if err != nil {
		t.Fatalf("minio host: %v", err)
	}
	mapped, err := minioContainer.MappedPort(ctx, "9000/tcp")
	if err != nil {
		t.Fatalf("minio port: %v", err)
	}
	freshBlobs, err := s3adapter.NewStore(s3adapter.Config{
		Endpoint:  host + ":" + mapped.Port(),
		Bucket:    "content-it",
		Region:    "us-east-1",
		AccessKey: "minioadmin",
		SecretKey: "minioadmin",
	})
	if err != nil {
		t.Fatalf("rebuilt blobs: %v", err)
	}
	recovered := adapterpg.NewStore(pool, cfg, freshBlobs, parquet.NewReader(freshBlobs))
	deadline := time.Now().Add(60 * time.Second)
	for {
		data, _, err := recovered.Get(ctx, tn, res.Artifact.ArtifactID)
		if err == nil {
			if string(data) != string(payload) {
				t.Fatal("recovered bytes must be intact")
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("minio did not recover in time: %v", err)
		}
		time.Sleep(500 * time.Millisecond)
	}
}
