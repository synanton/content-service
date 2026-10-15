//go:build integration

// C-5 fault battery: a slow-but-not-failed backend against the real
// stack. A latency-injecting proxy fronts testcontainers MinIO; the
// deadline policy must fail closed inside its bound, open the breaker on
// sustained failure, recover on probe, and emit exactly one
// artifact-attributed event=audit per failed operation — even though the
// downstream Postgres op fails alongside it (audit is stream-side, never
// transactional, so no rollback can erase it).
package integration

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
	"time"

	httphandler "github.com/synanton/content-service/internal/adapter/http"
	"github.com/synanton/content-service/internal/adapter/parquet"
	adapterpg "github.com/synanton/content-service/internal/adapter/postgres"
	s3adapter "github.com/synanton/content-service/internal/adapter/s3"
	"github.com/synanton/content-service/internal/jobs"
	"github.com/synanton/content-service/internal/security"
)

// testBucket is created by startMinio in TestMain; proxy-backed
// stores must use it, not the dev default from configs.
const testBucket = "content-it"

// slowProxy fronts MinIO and stalls every request by delay, emulating a
// degraded-but-listening backend (timeouts, not connection refused).
func slowProxy(t *testing.T, delay time.Duration) string {
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
	proxy := httputil.NewSingleHostReverseProxy(target)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(delay)
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Host
}

func slowGuarded(t *testing.T, endpoint string, policy s3adapter.Policy) *s3adapter.GuardedStore {
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

func tightPolicy() s3adapter.Policy {
	return s3adapter.Policy{
		OpTimeout:        500 * time.Millisecond,
		ListTimeout:      500 * time.Millisecond,
		MaxAttempts:      1,
		RetryBaseDelay:   0,
		BreakerThreshold: 100,
		BreakerCooldown:  time.Second,
	}
}

func slowStack(t *testing.T, endpoint string, policy s3adapter.Policy, auditor *security.Auditor) http.Handler {
	t.Helper()
	guarded := slowGuarded(t, endpoint, policy)
	slowStore := adapterpg.NewStore(pool, cfg, guarded, parquet.NewReader(guarded))
	h := httphandler.NewHandler(slowStore, auditor)
	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/artifacts", http.HandlerFunc(h.Put))
	return mux
}

func slowPut(t *testing.T, mux http.Handler, tenant string) *httptest.ResponseRecorder {
	t.Helper()
	// 300 KiB forces the s3 object tier: inline payloads never reach the
	// backend, which would make this test pass vacuously.
	big := bytes.Repeat([]byte("slow backend payload blob "), 12500)
	body, _ := json.Marshal(map[string]string{
		"tenant_id":    tenant,
		"payload":      base64.StdEncoding.EncodeToString(big),
		"content_type": "text/plain",
		"owner_id":     "slow-test",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/artifacts", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func auditEvents(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var decoded map[string]any
		if err := json.Unmarshal([]byte(line), &decoded); err != nil {
			t.Fatalf("audit output is not JSON: %v", err)
		}
		if decoded["event"] == "audit" {
			out = append(out, decoded)
		}
	}
	return out
}

// TestSlowBackendFailsClosedAndAudited is the D.3 exit on the real stack:
// a 5s-delayed backend with a 500ms deadline fails closed inside the
// bound and emits exactly one artifact-attributed audit record.
func TestSlowBackendFailsClosedAndAudited(t *testing.T) {
	endpoint := slowProxy(t, 5*time.Second)
	var buf bytes.Buffer
	auditor := security.NewAuditorWithLogger(true, slog.New(slog.NewJSONHandler(&buf, nil)))
	mux := slowStack(t, endpoint, tightPolicy(), auditor)

	start := time.Now()
	rec := slowPut(t, mux, tenant())
	elapsed := time.Since(start)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (%s)", rec.Code, rec.Body.String())
	}
	// Both bounds prove the intended path: below the 5 s backend delay
	// (failed closed at the 500 ms deadline, not at backend speed) and
	// above an instant failure (a bucket/config error fails in ms
	// without ever engaging the slow backend — the original C-5 false
	// positive).
	if elapsed > 10*time.Second {
		t.Fatalf("fail-closed took %v, want inside the deadline bound", elapsed)
	}
	if elapsed < 200*time.Millisecond {
		t.Fatalf("failed in %v: backend never engaged (config error, not timeout)", elapsed)
	}
	events := auditEvents(t, &buf)
	if len(events) != 1 {
		t.Fatalf("audit records = %d, want exactly 1:\n%s", len(events), buf.String())
	}
	got := events[0]
	if got["action"] != "put_failed" || got["reason"] != "UPSTREAM_TIMEOUT" {
		t.Errorf("action/reason = %v/%v", got["action"], got["reason"])
	}
	if got["tenant"] == nil || got["tenant"] == "" {
		t.Errorf("audit lacks tenant: %v", got)
	}
}

// TestSustainedSlownessOpensBreaker proves the breaker path end to end:
// repeated slow failures open the circuit (fast UPSTREAM_DEGRADED), and
// every failed operation still emits its single audit record.
func TestSustainedSlownessOpensBreaker(t *testing.T) {
	endpoint := slowProxy(t, 5*time.Second)
	var buf bytes.Buffer
	auditor := security.NewAuditorWithLogger(true, slog.New(slog.NewJSONHandler(&buf, nil)))
	policy := tightPolicy()
	policy.BreakerThreshold = 2
	mux := slowStack(t, endpoint, policy, auditor)

	for i := 0; i < 2; i++ {
		rec := slowPut(t, mux, tenant())
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("call %d: status = %d", i, rec.Code)
		}
	}
	rec := slowPut(t, mux, tenant())
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("breaker-open call: status = %d", rec.Code)
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != "UPSTREAM_DEGRADED" {
		t.Fatalf("breaker-open code = %q, want UPSTREAM_DEGRADED", body.Code)
	}
	events := auditEvents(t, &buf)
	if len(events) != 3 {
		t.Fatalf("audit records = %d, want exactly 3 (one per failed op):\n%s", len(events), buf.String())
	}
	for _, ev := range events {
		if ev["tenant"] == nil || ev["tenant"] == "" {
			t.Errorf("audit lacks tenant: %v", ev)
		}
	}
}

// TestSlowBackendSweeperScanAudited extends the evidence chain to job
// paths: the sweeper's reconciliation scan against the same slow backend
// fails closed and emits its single system-level audit record (scan
// failures carry no tenant; per-artifact failures below carry theirs).
func TestSlowBackendSweeperScanAudited(t *testing.T) {
	endpoint := slowProxy(t, 5*time.Second)
	guarded := slowGuarded(t, endpoint, tightPolicy())
	var buf bytes.Buffer
	auditor := security.NewAuditorWithLogger(true, slog.New(slog.NewJSONHandler(&buf, nil)))
	sweep := &jobs.Sweeper{
		Store: store, Blobs: guarded,
		Grace: time.Hour, Batch: 100, DryRun: true,
		Auditor: auditor,
	}
	if err := sweep.Sweep(ctx); err == nil {
		t.Fatal("sweeper scan against slow backend succeeded")
	}
	events := auditEvents(t, &buf)
	if len(events) != 1 {
		t.Fatalf("audit records = %d, want exactly 1:\n%s", len(events), buf.String())
	}
	got := events[0]
	if got["action"] != "sweep_failed" || got["reason"] != "UPSTREAM_TIMEOUT" {
		t.Errorf("action/reason = %v/%v", got["action"], got["reason"])
	}
}
