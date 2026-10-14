//go:build integration

// Benchmarks against real Postgres + MinIO. These measure wall-clock
// behavior of the wired stack, not algorithmic complexity: run them with
// `make bench-integration` (bounded iteration counts) and record results
// in docs/benchmarks.md with hardware provenance. Never gate PRs on
// absolute numbers — shared runners are too noisy for thresholds.
package integration

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	contentv1 "github.com/synanton/content-service/gen/content/v1"
	grpchandler "github.com/synanton/content-service/internal/adapter/grpc"
	httphandler "github.com/synanton/content-service/internal/adapter/http"
	"github.com/synanton/content-service/internal/jobs"
	"github.com/synanton/content-service/internal/port"
)

var benchTenantSeq atomic.Uint64

func benchTenant() string {
	return fmt.Sprintf("bench-%d", benchTenantSeq.Add(1))
}

func sizedPayload(seed byte, size int, salt uint64) []byte {
	payload := make([]byte, size)
	for i := range payload {
		payload[i] = seed + byte(i%251)
	}
	binary.BigEndian.PutUint64(payload, salt)
	return payload
}

type latencies struct {
	durs []time.Duration
}

func (l *latencies) add(d time.Duration) {
	l.durs = append(l.durs, d)
}

func (l *latencies) quantile(q float64) time.Duration {
	if len(l.durs) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), l.durs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[int(q*float64(len(sorted)-1))]
}

func (l *latencies) report(b *testing.B) {
	b.ReportMetric(float64(l.quantile(0.50).Microseconds())/1000.0, "p50-ms")
	b.ReportMetric(float64(l.quantile(0.95).Microseconds())/1000.0, "p95-ms")
	b.ReportMetric(float64(l.quantile(0.99).Microseconds())/1000.0, "p99-ms")
	b.Logf("ops=%d p50=%v p95=%v p99=%v", len(l.durs),
		l.quantile(0.50).Round(10*time.Microsecond),
		l.quantile(0.95).Round(10*time.Microsecond),
		l.quantile(0.99).Round(10*time.Microsecond))
}

func benchPut(b *testing.B, size int, seed byte) {
	b.Helper()
	tn := benchTenant()
	var lat latencies
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		start := time.Now()
		_, err := store.Put(ctx, port.PutParams{
			TenantID:      tn,
			Payload:       sizedPayload(seed, size, uint64(i)),
			ContentType:   "application/octet-stream",
			OwnerID:       "bench",
			RetentionDays: 30,
		})
		lat.add(time.Since(start))
		if err != nil {
			b.Fatalf("put: %v", err)
		}
	}
	b.StopTimer()
	lat.report(b)
}

func BenchmarkPutInline(b *testing.B)  { benchPut(b, 100, 'a') }
func BenchmarkPutParquet(b *testing.B) { benchPut(b, 5000, 'b') }
func BenchmarkPutS3(b *testing.B)      { benchPut(b, 300*1024, 'c') }

func BenchmarkPutDedupHit(b *testing.B) {
	tn := benchTenant()
	payload := sizedPayload('d', 5000, 0)
	if _, err := store.Put(ctx, port.PutParams{
		TenantID: tn, Payload: payload, OwnerID: "bench", RetentionDays: 30,
	}); err != nil {
		b.Fatalf("seed put: %v", err)
	}
	var lat latencies
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		start := time.Now()
		res, err := store.Put(ctx, port.PutParams{
			TenantID: tn, Payload: payload, OwnerID: "bench", RetentionDays: 30,
		})
		lat.add(time.Since(start))
		if err != nil {
			b.Fatalf("put: %v", err)
		}
		if !res.DedupHit {
			b.Fatal("expected dedup hit on identical republish")
		}
	}
	b.StopTimer()
	lat.report(b)
}

func benchGet(b *testing.B, size int, seed byte) {
	b.Helper()
	tn := benchTenant()
	res, err := store.Put(ctx, port.PutParams{
		TenantID: tn, Payload: sizedPayload(seed, size, 0),
		OwnerID: "bench", RetentionDays: 30,
	})
	if err != nil {
		b.Fatalf("seed put: %v", err)
	}
	var lat latencies
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		start := time.Now()
		if _, _, err := store.Get(ctx, tn, res.Artifact.ArtifactID); err != nil {
			b.Fatalf("get: %v", err)
		}
		lat.add(time.Since(start))
	}
	b.StopTimer()
	lat.report(b)
}

func BenchmarkGetInline(b *testing.B)  { benchGet(b, 100, 'e') }
func BenchmarkGetParquet(b *testing.B) { benchGet(b, 5000, 'f') }
func BenchmarkGetS3(b *testing.B)      { benchGet(b, 300*1024, 'g') }

func BenchmarkGetMissing(b *testing.B) {
	tn := benchTenant()
	var lat latencies
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		start := time.Now()
		_, _, _ = store.Get(ctx, tn, "00000000-0000-0000-0000-000000000000")
		lat.add(time.Since(start))
	}
	b.StopTimer()
	lat.report(b)
}

func httpBenchServer(b *testing.B) *httptest.Server {
	b.Helper()
	h := httphandler.NewHandler(store)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/artifacts", h.Put)
	mux.HandleFunc("GET /api/v1/artifacts/", h.Get)
	mux.HandleFunc("HEAD /api/v1/artifacts/", h.Head)
	mux.HandleFunc("GET /api/v1/capabilities", h.Capabilities)
	srv := httptest.NewServer(mux)
	b.Cleanup(srv.Close)
	return srv
}

func BenchmarkHttpPutGet(b *testing.B) {
	srv := httpBenchServer(b)
	tn := benchTenant()
	var lat latencies
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		body, _ := json.Marshal(map[string]interface{}{
			"tenant_id": tn,
			"payload":   base64.StdEncoding.EncodeToString(sizedPayload('h', 1024, uint64(i))),
		})
		start := time.Now()
		resp, err := http.Post(srv.URL+"/api/v1/artifacts", "application/json", bytes.NewReader(body))
		if err != nil {
			b.Fatalf("post: %v", err)
		}
		var put map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&put); err != nil {
			resp.Body.Close()
			b.Fatalf("decode put: %v", err)
		}
		resp.Body.Close()
		get, err := http.Get(srv.URL + "/api/v1/artifacts/" + put["artifact_id"].(string) + "?tenant_id=" + tn)
		if err != nil {
			b.Fatalf("get: %v", err)
		}
		_, _ = io.Copy(io.Discard, get.Body)
		get.Body.Close()
		if get.StatusCode != http.StatusOK {
			b.Fatalf("get status = %d", get.StatusCode)
		}
		lat.add(time.Since(start))
	}
	b.StopTimer()
	lat.report(b)
}

func BenchmarkGrpcPutGet(b *testing.B) {
	lis := bufconn.Listen(1 << 20)
	b.Cleanup(func() { _ = lis.Close() })
	srv := grpc.NewServer()
	b.Cleanup(srv.GracefulStop)
	contentv1.RegisterContentServiceServer(srv, grpchandler.NewServer(store))
	go func() { _ = srv.Serve(lis) }()
	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(dialCtx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(dialCtx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		b.Fatalf("dial: %v", err)
	}
	b.Cleanup(func() { _ = conn.Close() })
	client := contentv1.NewContentServiceClient(conn)
	tn := benchTenant()
	var lat latencies
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		start := time.Now()
		put, err := client.Put(ctx, &contentv1.PutRequest{
			TenantId: tn, Payload: sizedPayload('j', 1024, uint64(i)), OwnerId: "bench",
		})
		if err != nil {
			b.Fatalf("put: %v", err)
		}
		if _, err := client.Get(ctx, &contentv1.GetRequest{
			TenantId: tn, ArtifactId: put.GetArtifactId(),
		}); err != nil {
			b.Fatalf("get: %v", err)
		}
		lat.add(time.Since(start))
	}
	b.StopTimer()
	lat.report(b)
}

func BenchmarkCompactorMerge(b *testing.B) {
	compactor := &jobs.Compactor{Store: store, Blobs: blobs, TargetRows: 1000, MaxFiles: 32, Batch: 10000}
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		tn := benchTenant()
		for f := 0; f < 10; f++ {
			if _, err := store.Put(ctx, port.PutParams{
				TenantID: tn, Payload: sizedPayload(byte('k'+f), 5000, uint64(i*10+f)),
				OwnerID: "bench", RetentionDays: 30,
			}); err != nil {
				b.Fatalf("seed put: %v", err)
			}
		}
		b.StartTimer()
		start := time.Now()
		if err := compactor.Sweep(ctx); err != nil {
			b.Fatalf("sweep: %v", err)
		}
		b.Logf("merge-10-files=%v", time.Since(start))
		b.StopTimer()
	}
}

func BenchmarkDispositionSweep(b *testing.B) {
	sweep := &jobs.Disposition{Store: store, Blobs: blobs, Grace: time.Hour, Batch: 10000}
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		tn := benchTenant()
		res, err := store.Put(ctx, port.PutParams{
			TenantID: tn, Payload: []byte("disposition bench payload"),
			OwnerID: "bench", RetentionDays: 30,
		})
		if err != nil {
			b.Fatalf("seed put: %v", err)
		}
		if _, err := pool.Exec(ctx,
			`UPDATE retention_policies SET expires_at = now() - interval '2 hours'
			 WHERE tenant_id = $1 AND artifact_id = $2::uuid`,
			tn, res.Artifact.ArtifactID); err != nil {
			b.Fatalf("expire: %v", err)
		}
		if err := store.MarkForDeletion(ctx, tn, res.Artifact.ArtifactID, "bench"); err != nil {
			b.Fatalf("release: %v", err)
		}
		b.StartTimer()
		start := time.Now()
		if err := sweep.Sweep(ctx); err != nil {
			b.Fatalf("sweep: %v", err)
		}
		b.Logf("dispose-1-artifact=%v", time.Since(start))
		b.StopTimer()
	}
}

func BenchmarkIntegritySweep(b *testing.B) {
	tn := benchTenant()
	for i := 0; i < 50; i++ {
		if _, err := store.Put(ctx, port.PutParams{
			TenantID: tn, Payload: sizedPayload('m', 1024, uint64(i)),
			OwnerID: "bench", RetentionDays: 30,
		}); err != nil {
			b.Fatalf("seed put: %v", err)
		}
	}
	// One full sweep regardless of b.N: the sweep covers every tenant in
	// the database, so repeating it per N would measure N× the steady
	// state and take hours. The Logf line carries the real number.
	b.ResetTimer()
	start := time.Now()
	sweep := &jobs.Integrity{Store: store, Content: store, Batch: 10000, SampleRate: 1}
	result, err := sweep.Sweep(ctx)
	if err != nil {
		b.Fatalf("sweep: %v", err)
	}
	b.Logf("full-sweep checked=%d failed=%d duration=%v", result.Checked, result.Failed, time.Since(start))
	b.StopTimer()
}
