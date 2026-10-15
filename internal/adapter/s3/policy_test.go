package s3

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"

	"github.com/synanton/content-service/internal/port"
)

// scriptedTransport is a programmable backend: latency per call plus a
// per-call error script. Attempt counts prove retry behavior exactly.
type scriptedTransport struct {
	delay  time.Duration
	errs   []error
	calls  atomic.Int32
	onCall func()
	infos  []port.BlobInfo
}

func (f *scriptedTransport) script() error {
	n := int(f.calls.Load()) - 1
	if f.onCall != nil {
		f.onCall()
	}
	if n < len(f.errs) {
		return f.errs[n]
	}
	return nil
}

func (f *scriptedTransport) sleep(ctx context.Context) error {
	// Counted here, at transport entry: an attempt that dies waiting
	// still consumed its attempt.
	f.calls.Add(1)
	if f.delay <= 0 {
		return nil
	}
	t := time.NewTimer(f.delay)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (f *scriptedTransport) Put(ctx context.Context, key string, data []byte, contentType string) error {
	if err := f.sleep(ctx); err != nil {
		return err
	}
	return f.script()
}

func (f *scriptedTransport) Get(ctx context.Context, key string) ([]byte, error) {
	if err := f.sleep(ctx); err != nil {
		return nil, err
	}
	if err := f.script(); err != nil {
		return nil, err
	}
	return []byte("bytes"), nil
}

func (f *scriptedTransport) Delete(ctx context.Context, key string) error {
	if err := f.sleep(ctx); err != nil {
		return err
	}
	return f.script()
}

func (f *scriptedTransport) Exists(ctx context.Context, key string) (bool, error) {
	if err := f.sleep(ctx); err != nil {
		return false, err
	}
	if err := f.script(); err != nil {
		return false, err
	}
	return true, nil
}

func (f *scriptedTransport) GetStream(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	if err := f.sleep(ctx); err != nil {
		return nil, 0, err
	}
	if err := f.script(); err != nil {
		return nil, 0, err
	}
	return io.NopCloser(strings.NewReader("bytes")), 5, nil
}

func (f *scriptedTransport) Size(ctx context.Context, key string) (int64, error) {
	if err := f.sleep(ctx); err != nil {
		return 0, err
	}
	if err := f.script(); err != nil {
		return 0, err
	}
	return 5, nil
}

func (f *scriptedTransport) Range(ctx context.Context, key string, offset, length int64) ([]byte, error) {
	if err := f.sleep(ctx); err != nil {
		return nil, err
	}
	if err := f.script(); err != nil {
		return nil, err
	}
	return []byte("bytes"), nil
}

func (f *scriptedTransport) List(ctx context.Context, prefix string) ([]port.BlobInfo, error) {
	if err := f.sleep(ctx); err != nil {
		return nil, err
	}
	if err := f.script(); err != nil {
		return nil, err
	}
	return f.infos, nil
}

func (f *scriptedTransport) Bucket() string { return "test-bucket" }

func (f *scriptedTransport) EnsureBucket(ctx context.Context) error {
	if err := f.sleep(ctx); err != nil {
		return err
	}
	return f.script()
}

func err503() error {
	return minio.ErrorResponse{StatusCode: 503, Code: "SlowDown"}
}

func testPolicy() Policy {
	p := DefaultPolicy()
	p.OpTimeout = 50 * time.Millisecond
	p.ListTimeout = 50 * time.Millisecond
	p.RetryBaseDelay = time.Millisecond
	p.BreakerThreshold = 3
	p.BreakerCooldown = 50 * time.Millisecond
	return p
}

func guarded(t *testing.T, fake *scriptedTransport, p Policy) *GuardedStore {
	t.Helper()
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	return &GuardedStore{inner: fake, policy: p, breaker: newBreaker(p.BreakerThreshold, p.BreakerCooldown)}
}

func TestOpTimeoutFailsClosed(t *testing.T) {
	fake := &scriptedTransport{delay: 500 * time.Millisecond}
	g := guarded(t, fake, testPolicy())
	start := time.Now()
	err := g.Put(context.Background(), "k", []byte("v"), "text/plain")
	elapsed := time.Since(start)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("slow backend: err = %v, want DeadlineExceeded", err)
	}
	if elapsed > 300*time.Millisecond {
		t.Fatalf("deadline not enforced: took %v", elapsed)
	}
	if n := fake.calls.Load(); n != 1 {
		t.Fatalf("timed-out call retried: %d attempts", n)
	}
}

func TestRetryThenSuccess(t *testing.T) {
	fake := &scriptedTransport{errs: []error{err503(), err503()}}
	g := guarded(t, fake, testPolicy())
	if err := g.Put(context.Background(), "k", []byte("v"), "text/plain"); err != nil {
		t.Fatalf("transient 503s: %v", err)
	}
	if n := fake.calls.Load(); n != 3 {
		t.Fatalf("attempts = %d, want 3", n)
	}
}

func TestAttemptsExhausted(t *testing.T) {
	fake := &scriptedTransport{errs: []error{err503(), err503(), err503(), err503()}}
	g := guarded(t, fake, testPolicy())
	if err := g.Put(context.Background(), "k", []byte("v"), "text/plain"); err == nil {
		t.Fatal("persistent 503 accepted")
	}
	if n := fake.calls.Load(); n != 3 {
		t.Fatalf("attempts = %d, want MaxAttempts 3", n)
	}
}

func TestNoRetryOnNotFound(t *testing.T) {
	fake := &scriptedTransport{errs: []error{errNotFound("k")}}
	g := guarded(t, fake, testPolicy())
	if err := g.Delete(context.Background(), "k"); err == nil {
		t.Fatal("not-found accepted")
	}
	if n := fake.calls.Load(); n != 1 {
		t.Fatalf("not-found retried: %d attempts", n)
	}
	// MinIO 404 shape: surfaced, never retried.
	fakeM := &scriptedTransport{errs: []error{minio.ErrorResponse{StatusCode: 404, Code: "NoSuchKey"}}}
	gM := guarded(t, fakeM, testPolicy())
	if _, err := gM.Exists(context.Background(), "missing"); err == nil {
		t.Fatal("minio 404 accepted as success")
	}
	if n := fakeM.calls.Load(); n != 1 {
		t.Fatalf("minio 404 retried: %d attempts", n)
	}
}

func TestRetryOnTooManyRequests(t *testing.T) {
	// 429 under load retries like 5xx (minio SlowDown analog).
	fake := &scriptedTransport{errs: []error{
		minio.ErrorResponse{StatusCode: 429, Code: "TooManyRequests"},
	}}
	g := guarded(t, fake, testPolicy())
	if err := g.Put(context.Background(), "k", []byte("v"), "text/plain"); err != nil {
		t.Fatalf("single 429: %v", err)
	}
	if n := fake.calls.Load(); n != 2 {
		t.Fatalf("429 attempts = %d, want 2 (initial + one retry)", n)
	}
}

func TestNoRetryOnClientError(t *testing.T) {
	fake := &scriptedTransport{errs: []error{
		minio.ErrorResponse{StatusCode: 403, Code: "AccessDenied"},
	}}
	g := guarded(t, fake, testPolicy())
	if err := g.Put(context.Background(), "k", []byte("v"), "text/plain"); err == nil {
		t.Fatal("403 accepted")
	}
	if n := fake.calls.Load(); n != 1 {
		t.Fatalf("403 retried: %d attempts", n)
	}
}

func TestCallerBugsSkipPolicy(t *testing.T) {
	fake := &scriptedTransport{}
	g := guarded(t, fake, testPolicy())
	if err := g.Put(context.Background(), "", []byte("v"), "text/plain"); err == nil {
		t.Fatal("empty key accepted")
	}
	if _, err := g.Get(context.Background(), "/absolute"); err == nil {
		t.Fatal("absolute key accepted")
	}
	if n := fake.calls.Load(); n != 0 {
		t.Fatalf("caller bug consumed %d backend attempts", n)
	}
}

func TestBreakerOpensAndRecovers(t *testing.T) {
	// 9 scripted 503s: 3 guarded calls x 3 attempts each trip the
	// threshold-3 breaker; the 4th guarded call must fast-fail.
	errs := make([]error, 9)
	for i := range errs {
		errs[i] = err503()
	}
	fake := &scriptedTransport{errs: errs}
	g := guarded(t, fake, testPolicy())
	// Threshold is 3 consecutive guarded-call failures; each call burns
	// its 3 attempts against the persistent 503.
	for i := 0; i < 3; i++ {
		if err := g.Put(context.Background(), "k", []byte("v"), "text/plain"); err == nil {
			t.Fatalf("call %d accepted", i)
		}
	}
	before := fake.calls.Load()
	start := time.Now()
	err := g.Put(context.Background(), "k", []byte("v"), "text/plain")
	if !IsCircuitOpen(err) {
		t.Fatalf("breaker did not fast-fail: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("fast-fail took %v", elapsed)
	}
	if n := fake.calls.Load(); n != before {
		t.Fatal("open breaker touched the network")
	}
	// Past cooldown a probe is admitted; clear the script so it succeeds
	// and the breaker closes.
	time.Sleep(60 * time.Millisecond)
	fake.errs = nil
	fake.calls.Store(0)
	if err := g.Put(context.Background(), "k", []byte("v"), "text/plain"); err != nil {
		t.Fatalf("half-open probe: %v", err)
	}
	// Recovery needs exactly one success: the next failure must go to
	// the backend (attempts consumed, real error) rather than fast-fail.
	fake.errs = []error{err503(), err503(), err503(), err503()}
	fake.calls.Store(0)
	before = fake.calls.Load()
	err = g.Put(context.Background(), "k", []byte("v"), "text/plain")
	if err == nil || IsCircuitOpen(err) {
		t.Fatalf("post-recovery failure fast-failed: %v", err)
	}
	if n := fake.calls.Load(); n == before {
		t.Fatal("post-recovery failure did not reach the backend")
	}
}

func TestCallerCancelDoesNotTripBreaker(t *testing.T) {
	fake := &scriptedTransport{delay: 200 * time.Millisecond}
	g := guarded(t, fake, testPolicy())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := g.Put(ctx, "k", []byte("v"), "text/plain"); err == nil {
		t.Fatal("cancelled call accepted")
	}
	// Breaker must still be closed: a live call proceeds to the backend.
	fake.delay = 0
	if err := g.Put(context.Background(), "k", []byte("v"), "text/plain"); err != nil {
		t.Fatalf("breaker tripped by caller cancel: %v", err)
	}
}

func TestListUsesListTimeout(t *testing.T) {
	fake := &scriptedTransport{delay: 500 * time.Millisecond}
	p := testPolicy()
	p.OpTimeout = 5 * time.Second
	p.ListTimeout = 20 * time.Millisecond
	g := guarded(t, fake, p)
	start := time.Now()
	if _, err := g.List(context.Background(), ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("slow list: err = %v, want DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("list used op timeout instead of list timeout: %v", elapsed)
	}
}

func TestPolicyValidation(t *testing.T) {
	bad := DefaultPolicy()
	bad.OpTimeout = 0
	if _, err := (&Store{}).WithPolicy(bad); err == nil {
		t.Fatal("zero op timeout accepted")
	}
	if _, err := (&Store{}).WithPolicy(DefaultPolicy()); err != nil {
		t.Fatalf("default policy rejected: %v", err)
	}
}

// TestCircuitMarkingSurvivesWrapping proves the breaker signal reaches
// the HTTP/gRPC mappers intact through the postgres store's %w chains.
func TestCircuitMarkingSurvivesWrapping(t *testing.T) {
	fake := &scriptedTransport{errs: []error{err503()}}
	p := testPolicy()
	p.BreakerThreshold = 1
	p.MaxAttempts = 1
	g := guarded(t, fake, p)
	if err := g.Put(context.Background(), "k", []byte("v"), "text/plain"); err == nil {
		t.Fatal("503 accepted")
	}
	wrapped := g.Put(context.Background(), "k", []byte("v"), "text/plain")
	if !IsCircuitOpen(wrapped) {
		t.Fatalf("fast-fail not marked: %v", wrapped)
	}
	rewrapped := errorfWrap(wrapped)
	if !IsCircuitOpen(rewrapped) {
		t.Fatalf("marking lost through %%w chain: %v", rewrapped)
	}
}

func errorfWrap(err error) error {
	return &wrappedError{err: err}
}

type wrappedError struct{ err error }

func (e *wrappedError) Error() string { return "fetch object tier: " + e.err.Error() }
func (e *wrappedError) Unwrap() error { return e.err }

// TestOperationalEventsAreStructured proves the timeout and circuit
// records carry the event field C-5's fault test filters on. These are
// operational records (backend health), not actor-attributed audit:
// artifact-level audit stays in the postgres layer, which owns tenant
// context the S3 layer never sees.
func TestOperationalEventsAreStructured(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	fake := &scriptedTransport{delay: 300 * time.Millisecond}
	g := guarded(t, fake, testPolicy())
	_ = g.Put(context.Background(), "k", []byte("v"), "text/plain")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	found := false
	for _, line := range lines {
		var decoded map[string]any
		if err := json.Unmarshal([]byte(line), &decoded); err != nil {
			t.Fatalf("slog output is not JSON: %v", err)
		}
		if decoded["event"] == "s3.timeout" {
			found = true
			if decoded["op"] != "put" {
				t.Errorf("timeout record missing op: %v", decoded)
			}
		}
	}
	if !found {
		t.Fatalf("no event=s3.timeout record:\n%s", buf.String())
	}
}
