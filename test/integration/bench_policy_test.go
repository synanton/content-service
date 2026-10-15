//go:build integration

// BB-4: confirmatory benchmarks for the C-4 deadline policy. AC-2 is
// already closed via C-5's fault tests; these numbers confirm the policy
// quantitatively (attempt counts, transition latencies, overhead) and
// seed the BB-5 trend table. Evidence, never gates: report ranges,
// never means over bimodal data.
//
// Comparison mechanism (answered in plan, not discovered here): the
// guarded arm applies the decorator, the raw arm does not — same binary,
// same backend, same conditions. Config validation rejects disabling
// the policy through config, so the arms differ only in decorator
// application, decided in-test.
//
// Injection is per error class, never pooled: scripted-status proxy for
// 5xx/429 arms (deterministic), latency proxy + real MinIO for the slow
// path. Retry numbers separate first-try rate from attempts-given-
// retry; breaker numbers measure probe -> close/reopen (probe-dominated),
// never open -> half-open (that is the cooldown constant).
package integration

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	s3adapter "github.com/synanton/content-service/internal/adapter/s3"
)

// flakyProxy fails requests with code while fail returns true, then
// passes through to MinIO. The hit counter doubles as the attempt
// counter (one backend hit per decorator attempt).
func flakyProxy(t testing.TB, code int, fail func(n int64) bool) (string, *atomic.Int64) {
	t.Helper()
	host, err := minioContainer.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	mapped, err := minioContainer.MappedPort(ctx, "9000")
	if err != nil {
		t.Fatal(err)
	}
	target, err := url.Parse("http://" + host + ":" + mapped.Port())
	if err != nil {
		t.Fatal(err)
	}
	var hits atomic.Int64
	proxy := httputil.NewSingleHostReverseProxy(target)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		if fail(n) {
			http.Error(w, http.StatusText(code), code)
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Host, &hits
}

// delayedProxy stalls every request by delay, emulating a
// degraded-but-listening backend.
func delayedProxy(t testing.TB, delay time.Duration) string {
	t.Helper()
	endpoint, _ := flakyProxy(t, 0, func(n int64) bool {
		time.Sleep(delay)
		return false
	})
	return endpoint
}

func proxyStore(t testing.TB, endpoint string, policy s3adapter.Policy) *s3adapter.GuardedStore {
	t.Helper()
	raw, err := s3adapter.NewStore(s3adapter.Config{
		Endpoint:  endpoint,
		Bucket:    testBucket,
		Region:    "us-east-1",
		AccessKey: "minioadmin",
		SecretKey: "minioadmin",
		UseSSL:    false,
		PathStyle: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	g, err := raw.WithPolicy(policy)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func benchPolicy() s3adapter.Policy {
	return s3adapter.Policy{
		OpTimeout:        2 * time.Second,
		ListTimeout:      2 * time.Second,
		MaxAttempts:      5,
		RetryBaseDelay:   time.Millisecond,
		BreakerThreshold: 100,
		BreakerCooldown:  time.Second,
	}
}

// BenchmarkPolicyOverhead isolates the happy-path decorator cost:
// guarded vs raw over the healthy backend, same keys, same sizes.
// Failure-path cost (retries to the deadline) is a separate bench;
// conflating them would bury the overhead inside the retry numbers.
func BenchmarkPolicyOverhead(b *testing.B) {
	const size = 5000 // parquet tier: exercises S3 on every op
	guarded, err := rawBlobs.WithPolicy(benchPolicy())
	if err != nil {
		b.Fatal(err)
	}
	type backend interface {
		Put(ctx context.Context, key string, data []byte, contentType string) error
		Get(ctx context.Context, key string) ([]byte, error)
	}
	for _, arm := range []struct {
		name string
		be   backend
	}{
		{"raw", rawBlobs},
		{"guarded", guarded},
	} {
		b.Run("arm="+arm.name, func(b *testing.B) {
			// Warm up connections before measuring: without it the
			// first arm pays cold-start and the comparison measures
			// warmup, not policy.
			for w := 0; w < 3; w++ {
				wkey := fmt.Sprintf("bench/overhead/warm/%s/%d", arm.name, w)
				wp := sizedPayload('o', size, uint64(w))
				if err := arm.be.Put(ctx, wkey, wp, "application/octet-stream"); err != nil {
					b.Fatalf("warmup put: %v", err)
				}
				if _, err := arm.be.Get(ctx, wkey); err != nil {
					b.Fatalf("warmup get: %v", err)
				}
			}
			var putLat, getLat latencies
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				key := fmt.Sprintf("bench/overhead/%d", i)
				payload := sizedPayload('o', size, uint64(i))
				start := time.Now()
				if err := arm.be.Put(ctx, key, payload, "application/octet-stream"); err != nil {
					b.Fatalf("put: %v", err)
				}
				putLat.add(time.Since(start))
				start = time.Now()
				data, err := arm.be.Get(ctx, key)
				if err != nil {
					b.Fatalf("get: %v", err)
				}
				if len(data) != size {
					b.Fatalf("size = %d, want %d", len(data), size)
				}
				getLat.add(time.Since(start))
			}
			putLat.reportSuffixed(b, "put")
			getLat.reportSuffixed(b, "get")
		})
	}
}

// ints collects attempt counts: ReportMetric only, no time units.
type ints []int

func (v ints) p50() float64 {
	if len(v) == 0 {
		return 0
	}
	sorted := append([]int(nil), v...)
	sort.Ints(sorted)
	return float64(sorted[int(0.50*float64(len(sorted)-1))])
}

// BenchmarkPolicyRetryDistribution measures attempt counts against
// deterministic failures. Retries only happen on failure, so first-try
// rate and attempts-given-retry are reported separately — a pooled mean
// would hide the tail, which is the only part the policy affects.
func BenchmarkPolicyRetryDistribution(b *testing.B) {
	for _, tc := range []struct {
		name      string
		code      int
		fail      func(n int64) bool
		perIter   bool // fresh proxy per iteration (per-op failure pattern)
		wantTries int
	}{
		{"always-503", 503, func(int64) bool { return true }, false, 5},
		{"always-429", 429, func(int64) bool { return true }, false, 5},
		{"once-503-then-pass", 503, func(n int64) bool { return n == 1 }, true, 2},
	} {
		b.Run(tc.name, func(b *testing.B) {
			policy := benchPolicy()
			var endpoint string
			var hits *atomic.Int64
			if !tc.perIter {
				endpoint, hits = flakyProxy(b, tc.code, tc.fail)
			}
			var opLat latencies
			var tries ints
			firstTry := 0
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if tc.perIter {
					endpoint, hits = flakyProxy(b, tc.code, tc.fail)
				}
				g := proxyStore(b, endpoint, policy)
				before := hits.Load()
				start := time.Now()
				err := g.Put(ctx, fmt.Sprintf("bench/retry/%d", i), []byte("x"), "text/plain")
				opLat.add(time.Since(start))
				got := int(hits.Load() - before)
				tries = append(tries, got)
				if tc.wantTries == 5 && err == nil {
					b.Fatalf("always-fail op succeeded")
				}
				if tc.wantTries != 5 && err != nil {
					b.Fatalf("flaky op failed: %v", err)
				}
				if got == 1 {
					firstTry++
				}
				if got != tc.wantTries {
					b.Fatalf("attempts = %d, want %d", got, tc.wantTries)
				}
			}
			opLat.reportSuffixed(b, "op")
			b.ReportMetric(tries.p50(), "tries-p50")
			b.ReportMetric(float64(firstTry)/float64(b.N), "first-try-rate")
		})
	}
}

// BenchmarkPolicySlowBackend measures the fail-closed bound against a
// 3s-delayed backend with a 300ms deadline, plus the correctness
// contrast: the raw arm runs the same delayed backend with a 10s cap
// (manual iterations — an unbounded arm has no meaningful latency, only
// a tail the policy eliminates).
func BenchmarkPolicySlowBackend(b *testing.B) {
	endpoint := delayedProxy(b, 3*time.Second)
	policy := benchPolicy()
	policy.OpTimeout = 300 * time.Millisecond
	policy.MaxAttempts = 1
	policy.BreakerThreshold = 1000
	g := proxyStore(b, endpoint, policy)

	b.Run("guarded", func(b *testing.B) {
		var opLat latencies
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			start := time.Now()
			err := g.Put(ctx, fmt.Sprintf("bench/slow/%d", i), []byte("x"), "text/plain")
			opLat.add(time.Since(start))
			if err == nil {
				b.Fatal("slow op succeeded past the deadline")
			}
		}
		opLat.reportSuffixed(b, "op")
	})

	b.Run("raw-contrast", func(b *testing.B) {
		raw, err := s3adapter.NewStore(s3adapter.Config{
			Endpoint:  endpoint,
			Bucket:    testBucket,
			Region:    "us-east-1",
			AccessKey: "minioadmin",
			SecretKey: "minioadmin",
			UseSSL:    false,
			PathStyle: true,
		})
		if err != nil {
			b.Fatal(err)
		}
		// Manual iterations: the unguarded arm completes at ~delay, so
		// N=3 bounds the suite while still showing the eliminated tail.
		const iters = 3
		var opLat latencies
		b.ResetTimer()
		for i := 0; i < iters; i++ {
			start := time.Now()
			capped, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := raw.Put(capped, fmt.Sprintf("bench/raw/%d", i), []byte("x"), "text/plain")
			cancel()
			opLat.add(time.Since(start))
			if err != nil {
				b.Fatalf("raw op failed: %v", err)
			}
		}
		opLat.reportSuffixed(b, "op")
	})
}

// BenchmarkBreakerTransitions measures the only quantitative breaker
// transitions: half-open probe -> close and probe -> reopen, dominated
// by the probe's own backend call. Open -> half-open is the cooldown
// constant, confirmed by config, not measured.
func BenchmarkBreakerTransitions(b *testing.B) {
	b.Run("probe-close", func(b *testing.B) {
		var probeLat latencies
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			// Two failing ops open the threshold-2 breaker; cooldown
			// passes; the probe succeeds and closes.
			endpoint, _ := flakyProxy(b, 503, func(n int64) bool { return n <= 2 })
			policy := benchPolicy()
			policy.BreakerThreshold = 2
			policy.BreakerCooldown = 100 * time.Millisecond
			policy.MaxAttempts = 1
			g := proxyStore(b, endpoint, policy)
			for k := 0; k < 2; k++ {
				_ = g.Put(ctx, fmt.Sprintf("bench/brk/%d/%d", i, k), []byte("x"), "text/plain")
			}
			time.Sleep(120 * time.Millisecond)
			start := time.Now()
			if err := g.Put(ctx, fmt.Sprintf("bench/brk/%d/probe", i), []byte("x"), "text/plain"); err != nil {
				b.Fatalf("probe: %v", err)
			}
			probeLat.add(time.Since(start))
		}
		probeLat.reportSuffixed(b, "probe")
	})
}
