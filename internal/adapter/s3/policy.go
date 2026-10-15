package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/minio/minio-go/v7"

	"github.com/synanton/content-service/internal/port"
)

// Policy bounds every object-storage call in time, retries transient
// backend failures within that bound, and fails fast through a circuit
// breaker when the backend is down. A slow-but-not-failed backend fails
// closed (context deadline) instead of stalling callers.
type Policy struct {
	// OpTimeout bounds point operations: Put, Get, Stat, Delete, Range.
	OpTimeout time.Duration
	// ListTimeout bounds reconciliation scans, which legitimately run longer.
	ListTimeout time.Duration
	// MaxAttempts counts total tries (1 initial + retries).
	MaxAttempts int
	// RetryBaseDelay is the first backoff; it doubles per try.
	RetryBaseDelay time.Duration
	// BreakerThreshold consecutive backend failures open the breaker.
	BreakerThreshold int
	// BreakerCooldown is how long an open breaker fails fast before a probe.
	BreakerCooldown time.Duration
}

// DefaultPolicy mirrors the config defaults. Prefer config loading;
// use this for tests and tools that construct a Store directly.
func DefaultPolicy() Policy {
	return Policy{
		OpTimeout:        30 * time.Second,
		ListTimeout:      2 * time.Minute,
		MaxAttempts:      3,
		RetryBaseDelay:   200 * time.Millisecond,
		BreakerThreshold: 5,
		BreakerCooldown:  30 * time.Second,
	}
}

// Validate rejects incoherent policies before serving.
func (p Policy) Validate() error {
	if p.OpTimeout <= 0 {
		return fmt.Errorf("s3 policy: op timeout must be positive")
	}
	if p.ListTimeout <= 0 {
		return fmt.Errorf("s3 policy: list timeout must be positive")
	}
	if p.MaxAttempts < 1 {
		return fmt.Errorf("s3 policy: max attempts must be at least 1")
	}
	if p.RetryBaseDelay < 0 {
		return fmt.Errorf("s3 policy: retry base delay must be non-negative")
	}
	if p.BreakerThreshold < 1 {
		return fmt.Errorf("s3 policy: breaker threshold must be at least 1")
	}
	if p.BreakerCooldown < 0 {
		return fmt.Errorf("s3 policy: breaker cooldown must be non-negative")
	}
	return nil
}

// circuitOpenError is returned without touching the network while the
// breaker is open. Callers treat it as a fast, retryable-later failure.
type circuitOpenError struct {
	op string
}

func (e *circuitOpenError) Error() string {
	return "s3: circuit open for " + e.op + " (backend degraded)"
}

// IsCircuitOpen reports a breaker fast-fail, unwrapping as needed.
func IsCircuitOpen(err error) bool {
	var open *circuitOpenError
	return errors.As(err, &open)
}

// breaker is a consecutive-failure circuit breaker with a half-open
// probe. It is not a rate limiter and carries no request state.
type breaker struct {
	mu          sync.Mutex
	threshold   int
	cooldown    time.Duration
	consecutive int
	openedAt    time.Time
}

func newBreaker(threshold int, cooldown time.Duration) *breaker {
	return &breaker{threshold: threshold, cooldown: cooldown}
}

// allow reports whether a call may proceed. Past cooldown an open
// breaker admits exactly one probe; the probe's outcome re-arms or
// re-opens it via success/failure.
func (b *breaker) allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.consecutive < b.threshold {
		return true
	}
	if time.Since(b.openedAt) >= b.cooldown {
		return true
	}
	return false
}

func (b *breaker) success() {
	b.mu.Lock()
	defer b.mu.Unlock()
	wasOpen := b.consecutive >= b.threshold
	b.consecutive = 0
	if wasOpen {
		slog.Info("s3 circuit closed", "event", "s3.circuit", "state", "closed")
	}
}

func (b *breaker) failure(op string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.consecutive++
	if b.consecutive == b.threshold {
		b.openedAt = time.Now()
		slog.Warn("s3 circuit opened", "event", "s3.circuit", "state", "open",
			"op", op, "threshold", b.threshold, "cooldown", b.cooldown.String())
	}
}

// transport is the backend surface GuardedStore enforces Policy over.
// *Store implements it; tests substitute a scripted fake.
type transport interface {
	Put(ctx context.Context, key string, data []byte, contentType string) error
	Get(ctx context.Context, key string) ([]byte, error)
	Delete(ctx context.Context, key string) error
	Exists(ctx context.Context, key string) (bool, error)
	GetStream(ctx context.Context, key string) (io.ReadCloser, int64, error)
	Size(ctx context.Context, key string) (int64, error)
	Range(ctx context.Context, key string, offset, length int64) ([]byte, error)
	List(ctx context.Context, prefix string) ([]port.BlobInfo, error)
	Bucket() string
	EnsureBucket(ctx context.Context) error
}

// GuardedStore is a port.BlobStore (+ reader/lister/range) that enforces
// Policy around an S3 Store. Construct with Store.WithPolicy; never with
// a partially configured Policy (WithPolicy validates).
type GuardedStore struct {
	inner   transport
	policy  Policy
	breaker *breaker
}

// WithPolicy wraps the Store with deadline, retry, and breaker
// enforcement. The Store keeps serving unwrapped for callers that manage
// their own bounds (there are none in this repo; the wrapper is the only
// wiring in main and jobsboot).
func (s *Store) WithPolicy(p Policy) (*GuardedStore, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &GuardedStore{
		inner:   s,
		policy:  p,
		breaker: newBreaker(p.BreakerThreshold, p.BreakerCooldown),
	}, nil
}

// Bucket passes through (no network, no policy).
func (s *GuardedStore) Bucket() string { return s.inner.Bucket() }

// EnsureBucket creates the bucket when missing. Bounded by the op
// timeout; startup must fail fast, so it neither retries nor consults
// the breaker.
func (s *GuardedStore) EnsureBucket(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, s.policy.OpTimeout)
	defer cancel()
	return s.inner.EnsureBucket(ctx)
}

func (s *GuardedStore) Put(ctx context.Context, key string, data []byte, contentType string) error {
	if err := checkKey(key); err != nil {
		return err
	}
	return s.do(ctx, "put", s.policy.OpTimeout, func(ctx context.Context) error {
		return s.inner.Put(ctx, key, data, contentType)
	})
}

func (s *GuardedStore) Get(ctx context.Context, key string) ([]byte, error) {
	if err := checkKey(key); err != nil {
		return nil, err
	}
	var out []byte
	err := s.do(ctx, "get", s.policy.OpTimeout, func(ctx context.Context) error {
		data, err := s.inner.Get(ctx, key)
		if err != nil {
			return err
		}
		out = data
		return nil
	})
	return out, err
}

func (s *GuardedStore) Delete(ctx context.Context, key string) error {
	if err := checkKey(key); err != nil {
		return err
	}
	return s.do(ctx, "delete", s.policy.OpTimeout, func(ctx context.Context) error {
		return s.inner.Delete(ctx, key)
	})
}

func (s *GuardedStore) Exists(ctx context.Context, key string) (bool, error) {
	if err := checkKey(key); err != nil {
		return false, err
	}
	var out bool
	err := s.do(ctx, "exists", s.policy.OpTimeout, func(ctx context.Context) error {
		ok, err := s.inner.Exists(ctx, key)
		if err != nil {
			return err
		}
		out = ok
		return nil
	})
	return out, err
}

// GetStream opens the object for streaming reads. The deadline bounds
// the open (GetObject + Stat); subsequent reads inherit the caller's
// context, so a slow consumer is the caller's responsibility, not the
// backend's.
func (s *GuardedStore) GetStream(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	if err := checkKey(key); err != nil {
		return nil, 0, err
	}
	var rc io.ReadCloser
	var size int64
	err := s.do(ctx, "get-stream", s.policy.OpTimeout, func(ctx context.Context) error {
		stream, n, err := s.inner.GetStream(ctx, key)
		if err != nil {
			return err
		}
		rc, size = stream, n
		return nil
	})
	return rc, size, err
}

// Size returns the object size without fetching bytes.
func (s *GuardedStore) Size(ctx context.Context, key string) (int64, error) {
	if err := checkKey(key); err != nil {
		return 0, err
	}
	var out int64
	err := s.do(ctx, "size", s.policy.OpTimeout, func(ctx context.Context) error {
		n, err := s.inner.Size(ctx, key)
		if err != nil {
			return err
		}
		out = n
		return nil
	})
	return out, err
}

// Range fetches [offset, offset+length) bytes.
func (s *GuardedStore) Range(ctx context.Context, key string, offset, length int64) ([]byte, error) {
	if err := checkKey(key); err != nil {
		return nil, err
	}
	if offset < 0 || length <= 0 {
		return nil, fmt.Errorf("s3: invalid range %d+%d for %q", offset, length, key)
	}
	var out []byte
	err := s.do(ctx, "range", s.policy.OpTimeout, func(ctx context.Context) error {
		data, err := s.inner.Range(ctx, key, offset, length)
		if err != nil {
			return err
		}
		out = data
		return nil
	})
	return out, err
}

// List enumerates objects under prefix for reconciliation scans. Bounded
// by the (longer) list timeout; the whole enumeration is one guarded call.
func (s *GuardedStore) List(ctx context.Context, prefix string) ([]port.BlobInfo, error) {
	var out []port.BlobInfo
	err := s.do(ctx, "list", s.policy.ListTimeout, func(ctx context.Context) error {
		infos, err := s.inner.List(ctx, prefix)
		if err != nil {
			return err
		}
		out = infos
		return nil
	})
	return out, err
}

// checkKey mirrors the transport's key validation so caller bugs fail
// immediately: no attempts consumed, no breaker signal polluted.
func checkKey(key string) error {
	if key == "" {
		return fmt.Errorf("s3: key is required")
	}
	if len(key) > 0 && key[0] == '/' {
		return fmt.Errorf("s3: key must be relative, got %q", key)
	}
	return nil
}

// do runs attempt under the breaker and a single overall deadline that
// covers all attempts including backoffs: slow backends fail closed at
// the bound, never at attempts × bound. Success resets the breaker; a
// backend failure records one signal however many attempts ran. The
// breaker is per GuardedStore (shared across operations): a dead backend
// trips List and Get together, which matches the failure mode (backend
// down, not operation down) and keeps BB-4 breaker-open events
// unambiguous. Half-open recovery needs exactly one successful probe.
func (s *GuardedStore) do(ctx context.Context, op string, timeout time.Duration, attempt func(ctx context.Context) error) error {
	if !s.breaker.allow() {
		return &circuitOpenError{op: op}
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	deadline, _ := callCtx.Deadline()
	delay := s.policy.RetryBaseDelay
	var err error
	for n := 1; n <= s.policy.MaxAttempts; n++ {
		err = attempt(callCtx)
		if err == nil {
			s.breaker.success()
			return nil
		}
		// Caller cancelled: propagate without a breaker signal. A
		// disconnecting client is not backend failure, and counting it
		// would let callers trip the breaker spuriously.
		if ctx.Err() != nil {
			return fmt.Errorf("s3: %s: %w", op, ctx.Err())
		}
		// Our deadline held while the backend did not answer: fail
		// closed and record the backend signal.
		if callCtx.Err() != nil {
			s.breaker.failure(op)
			slog.Warn("s3 operation timed out", "event", "s3.timeout",
				"op", op, "timeout", timeout.String())
			return fmt.Errorf("s3: %s timed out after %v: %w", op, timeout, context.DeadlineExceeded)
		}
		if !retryable(err) {
			s.breaker.failure(op)
			return err
		}
		if n < s.policy.MaxAttempts {
			wait := backoff(delay, n)
			// Deadline-aware backoff: never burn an attempt that cannot
			// fit its budget. Returning the last real error keeps
			// attempt-count evidence clean (no guaranteed-timeout tail).
			if time.Until(deadline) < wait {
				s.breaker.failure(op)
				return fmt.Errorf("s3: %s deadline short of backoff after %d attempts: %w", op, n, err)
			}
			slog.Debug("s3 operation retrying", "op", op, "attempt", n+1, "err", err)
			if !sleepOrDone(callCtx, wait) {
				if ctx.Err() != nil {
					return fmt.Errorf("s3: %s: %w", op, ctx.Err())
				}
				s.breaker.failure(op)
				return fmt.Errorf("s3: %s timed out in backoff after %d attempts: %w", op, n, context.DeadlineExceeded)
			}
		}
	}
	s.breaker.failure(op)
	return fmt.Errorf("s3: %s failed after %d attempts: %w", op, s.policy.MaxAttempts, err)
}

func backoff(base time.Duration, attempt int) time.Duration {
	d := base
	for i := 1; i < attempt; i++ {
		d *= 2
	}
	return d
}

func sleepOrDone(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// retryable reports transient backend failures: 5xx (including 503
// SlowDown), 429 TooManyRequests under load, and transport-level errors.
// Missing objects, other 4xx, and local validation errors never retry:
// retrying them burns the budget and pollutes the breaker for a caller bug.
func retryable(err error) bool {
	if err == nil {
		return false
	}
	if IsNotFound(err) {
		return false
	}
	var resp minio.ErrorResponse
	if errors.As(err, &resp) {
		if resp.StatusCode == 429 {
			return true
		}
		if resp.StatusCode >= 500 && resp.StatusCode < 600 {
			return true
		}
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	// Unknown shape from the transport: fail closed without retry. A
	// retryable new shape must be classified here explicitly.
	return false
}
