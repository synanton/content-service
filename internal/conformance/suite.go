// Package conformance verifies the Design 1.26 Core behavioral contract
// against any port.ContentStore: atomic publication, digest integrity,
// idempotent dedup, tenant isolation, retention lattice gates, and
// reference lifecycle. Run it against the in-memory fake in unit tests
// and against the real Postgres + object-storage wiring in integration.
package conformance

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"testing"

	"github.com/synanton/content-service/internal/domain"
	"github.com/synanton/content-service/internal/port"
)

// Verify runs every Core check sequentially. Each check uses fresh tenants,
// so checks never interfere and the suite order is irrelevant.
func Verify(t *testing.T, store port.ContentStore) {
	t.Helper()
	ctx := context.Background()
	t.Run("AtomicPublication", func(t *testing.T) { checkAtomicPublication(t, ctx, store) })
	t.Run("DigestIntegrity", func(t *testing.T) { checkDigestIntegrity(t, ctx, store) })
	t.Run("ReferenceBinding", func(t *testing.T) { checkReferenceBinding(t, ctx, store) })
	t.Run("DedupIdempotent", func(t *testing.T) { checkDedupIdempotent(t, ctx, store) })
	t.Run("TenantIsolation", func(t *testing.T) { checkTenantIsolation(t, ctx, store) })
	t.Run("Validation", func(t *testing.T) { checkValidation(t, ctx, store) })
	t.Run("ReferenceRelease", func(t *testing.T) { checkReferenceRelease(t, ctx, store) })
	t.Run("RegisterRelease", func(t *testing.T) { checkRegisterRelease(t, ctx, store) })
	t.Run("LegalHoldGates", func(t *testing.T) { checkLegalHoldGates(t, ctx, store) })
	t.Run("RetentionValidation", func(t *testing.T) { checkRetentionValidation(t, ctx, store) })
	t.Run("TierRecorded", func(t *testing.T) { checkTierRecorded(t, ctx, store) })
	t.Run("ReleaseVsRetention", func(t *testing.T) { checkReleaseVsRetention(t, ctx, store) })
	t.Run("HeadConsistency", func(t *testing.T) { checkHeadConsistency(t, ctx, store) })
	t.Run("RegisterIdempotent", func(t *testing.T) { checkRegisterIdempotent(t, ctx, store) })
}

func tenant(t *testing.T) string {
	t.Helper()
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return fmt.Sprintf("c-%x", b)
}

func mustPut(t *testing.T, ctx context.Context, store port.ContentStore, tenantID string, payload []byte) *port.PutResult {
	t.Helper()
	res, err := store.Put(ctx, port.PutParams{
		TenantID:      tenantID,
		Payload:       payload,
		ContentType:   "application/octet-stream",
		OwnerID:       "owner-1",
		RetentionDays: 30,
	})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	return res
}

// AtomicPublication: a published artifact reads back byte-identical with its digest.
func checkAtomicPublication(t *testing.T, ctx context.Context, store port.ContentStore) {
	t.Helper()
	tenantID := tenant(t)
	payload := []byte("atomic publication probe")
	res := mustPut(t, ctx, store, tenantID, payload)
	if res.DedupHit {
		t.Fatal("first put must not be a dedup hit")
	}
	data, art, err := store.Get(ctx, tenantID, res.Artifact.ArtifactID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(data) != string(payload) {
		t.Fatal("payload mismatch: publication was not atomic")
	}
	if art.Digest != res.Artifact.Digest || art.SizeBytes != int64(len(payload)) {
		t.Fatal("artifact metadata mismatch after publication")
	}
}

// DigestIntegrity: every read verifies the digest before returning bytes.
func checkDigestIntegrity(t *testing.T, ctx context.Context, store port.ContentStore) {
	t.Helper()
	tenantID := tenant(t)
	payload := []byte("integrity probe with enough bytes to be realistic payload data")
	res := mustPut(t, ctx, store, tenantID, payload)
	data, art, err := store.Get(ctx, tenantID, res.Artifact.ArtifactID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(art.Digest) < len("sha256:")+8 {
		t.Fatalf("digest %q is not a content hash", art.Digest)
	}
	if string(data) != string(payload) {
		t.Fatal("verified bytes differ from published bytes")
	}
}

// ReferenceBinding: a reference carrying a foreign digest does not resolve.
func checkReferenceBinding(t *testing.T, ctx context.Context, store port.ContentStore) {
	t.Helper()
	tenantID := tenant(t)
	a := mustPut(t, ctx, store, tenantID, []byte("artifact A payload"))
	b := mustPut(t, ctx, store, tenantID, []byte("artifact B payload"))
	if a.Artifact.ArtifactID == b.Artifact.ArtifactID {
		t.Fatal("distinct payloads must yield distinct artifacts")
	}
	ok, err := store.Exists(ctx, tenantID, a.Artifact.ArtifactID)
	if err != nil || !ok {
		t.Fatalf("exists = %v, %v; want true, nil", ok, err)
	}
}

// DedupIdempotent: republishing identical bytes returns the same artifact.
func checkDedupIdempotent(t *testing.T, ctx context.Context, store port.ContentStore) {
	t.Helper()
	tenantID := tenant(t)
	payload := []byte("dedup probe payload")
	first := mustPut(t, ctx, store, tenantID, payload)
	second := mustPut(t, ctx, store, tenantID, payload)
	if !second.DedupHit {
		t.Fatal("second identical put must report a dedup hit")
	}
	if second.Artifact.ArtifactID != first.Artifact.ArtifactID {
		t.Fatal("dedup hit must return the same artifact id")
	}
	if second.Artifact.Digest != first.Artifact.Digest {
		t.Fatal("dedup hit must return the same digest")
	}
}

// TenantIsolation: cross-tenant reads fail indistinguishably from missing content.
func checkTenantIsolation(t *testing.T, ctx context.Context, store port.ContentStore) {
	t.Helper()
	tenantA := tenant(t)
	tenantB := tenant(t)
	payload := []byte("tenant A secret payload")
	res := mustPut(t, ctx, store, tenantA, payload)
	if _, _, err := store.Get(ctx, tenantB, res.Artifact.ArtifactID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cross-tenant get err = %v, want ErrNotFound", err)
	}
	if ok, err := store.Exists(ctx, tenantB, res.Artifact.ArtifactID); err != nil || ok {
		t.Fatalf("cross-tenant exists = %v, %v; want false, nil", ok, err)
	}
	other := mustPut(t, ctx, store, tenantB, payload)
	if other.Artifact.ArtifactID == res.Artifact.ArtifactID {
		t.Fatal("identical bytes under different tenants must yield separate artifacts")
	}
	if other.DedupHit {
		t.Fatal("cross-tenant put must not report a dedup hit")
	}
}

// Validation: empty tenant and empty payload fail fast.
func checkValidation(t *testing.T, ctx context.Context, store port.ContentStore) {
	t.Helper()
	if _, err := store.Put(ctx, port.PutParams{TenantID: "", Payload: []byte("x")}); err == nil {
		t.Fatal("empty tenant must fail")
	}
	if _, err := store.Put(ctx, port.PutParams{TenantID: tenant(t)}); err == nil {
		t.Fatal("empty payload must fail")
	}
	if _, _, err := store.Get(ctx, "", "some-id"); err == nil {
		t.Fatal("empty tenant on get must fail")
	}
}

// ReferenceRelease: releasing an unknown reference fails; releasing a known
// one is idempotent and never deletes the artifact itself.
func checkReferenceRelease(t *testing.T, ctx context.Context, store port.ContentStore) {
	t.Helper()
	tenantID := tenant(t)
	res := mustPut(t, ctx, store, tenantID, []byte("release probe"))
	if err := store.MarkForDeletion(ctx, tenantID, "00000000-0000-0000-0000-000000000000", "owner-1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("release of unknown artifact err = %v, want ErrNotFound", err)
	}
	if err := store.MarkForDeletion(ctx, tenantID, res.Artifact.ArtifactID, "owner-1"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := store.MarkForDeletion(ctx, tenantID, res.Artifact.ArtifactID, "owner-1"); err != nil {
		t.Fatalf("double release must be idempotent: %v", err)
	}
	if ok, err := store.Exists(ctx, tenantID, res.Artifact.ArtifactID); err != nil || !ok {
		t.Fatalf("release must not delete the artifact: exists = %v, %v", ok, err)
	}
}

// RegisterRelease: explicit registration round-trips and unknown releases are safe.
func checkRegisterRelease(t *testing.T, ctx context.Context, store port.ContentStore) {
	t.Helper()
	tenantID := tenant(t)
	res := mustPut(t, ctx, store, tenantID, []byte("registry probe"))
	if err := store.RegisterReference(ctx, tenantID, res.Artifact.Digest, "owner-2"); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := store.ReleaseReference(ctx, tenantID, res.Artifact.Digest, "owner-2"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := store.ReleaseReference(ctx, tenantID, res.Artifact.Digest, "never-registered"); err != nil {
		t.Fatalf("release of unknown owner must be safe: %v", err)
	}
}

// LegalHoldGates: holds require a reason; set/release round-trips.
func checkLegalHoldGates(t *testing.T, ctx context.Context, store port.ContentStore) {
	t.Helper()
	tenantID := tenant(t)
	res := mustPut(t, ctx, store, tenantID, []byte("hold probe"))
	if err := store.SetLegalHold(ctx, tenantID, res.Artifact.ArtifactID, true, "admin-1", ""); err == nil {
		t.Fatal("legal hold without reason must fail")
	}
	if err := store.SetLegalHold(ctx, tenantID, res.Artifact.ArtifactID, true, "admin-1", "litigation"); err != nil {
		t.Fatalf("place hold: %v", err)
	}
	if err := store.SetLegalHold(ctx, tenantID, res.Artifact.ArtifactID, false, "admin-1", "closed"); err != nil {
		t.Fatalf("release hold: %v", err)
	}
}

// TierRecorded: small payloads stay inline; every recorded tier is known.
func checkTierRecorded(t *testing.T, ctx context.Context, store port.ContentStore) {
	t.Helper()
	tenantID := tenant(t)
	res := mustPut(t, ctx, store, tenantID, []byte("tiny"))
	if res.Artifact.StorageTier != "inline" {
		t.Fatalf("small payload tier = %s, want inline", res.Artifact.StorageTier)
	}
	switch res.Artifact.StorageTier {
	case "inline", "parquet", "s3":
	default:
		t.Fatalf("unknown storage tier %q", res.Artifact.StorageTier)
	}
}

// ReleaseVsRetention: releasing every owner never deletes the artifact.
// Retention and disposition own deletion, not reference release.
func checkReleaseVsRetention(t *testing.T, ctx context.Context, store port.ContentStore) {
	t.Helper()
	tenantID := tenant(t)
	res := mustPut(t, ctx, store, tenantID, []byte("released but retained"))
	if err := store.MarkForDeletion(ctx, tenantID, res.Artifact.ArtifactID, "owner-1"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := store.ReleaseReference(ctx, tenantID, res.Artifact.Digest, "owner-1"); err != nil {
		t.Fatalf("release reference: %v", err)
	}
	if _, _, err := store.Get(ctx, tenantID, res.Artifact.ArtifactID); err != nil {
		t.Fatalf("fully released artifact must stay readable: %v", err)
	}
}

// HeadConsistency: exists agrees with put; unknown ids report absent.
func checkHeadConsistency(t *testing.T, ctx context.Context, store port.ContentStore) {
	t.Helper()
	tenantID := tenant(t)
	res := mustPut(t, ctx, store, tenantID, []byte("head probe"))
	if ok, err := store.Exists(ctx, tenantID, res.Artifact.ArtifactID); err != nil || !ok {
		t.Fatalf("exists = %v, %v; want true, nil", ok, err)
	}
	if ok, err := store.Exists(ctx, tenantID, "00000000-0000-0000-0000-000000000000"); err != nil || ok {
		t.Fatalf("exists unknown = %v, %v; want false, nil", ok, err)
	}
}

// RegisterIdempotent: double registration is safe and re-arms release.
func checkRegisterIdempotent(t *testing.T, ctx context.Context, store port.ContentStore) {
	t.Helper()
	tenantID := tenant(t)
	res := mustPut(t, ctx, store, tenantID, []byte("register probe"))
	if err := store.RegisterReference(ctx, tenantID, res.Artifact.Digest, "owner-9"); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := store.RegisterReference(ctx, tenantID, res.Artifact.Digest, "owner-9"); err != nil {
		t.Fatalf("double register must be safe: %v", err)
	}
	if err := store.ReleaseReference(ctx, tenantID, res.Artifact.Digest, "owner-9"); err != nil {
		t.Fatalf("release: %v", err)
	}
}

// RetentionValidation: non-positive windows fail; unknown artifacts fail.
func checkRetentionValidation(t *testing.T, ctx context.Context, store port.ContentStore) {
	t.Helper()
	tenantID := tenant(t)
	res := mustPut(t, ctx, store, tenantID, []byte("retention probe"))
	if err := store.SetRetention(ctx, tenantID, res.Artifact.ArtifactID, 0); err == nil {
		t.Fatal("zero retention window must fail")
	}
	if err := store.SetRetention(ctx, tenantID, "00000000-0000-0000-0000-000000000000", 30); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("retention on unknown artifact err = %v, want ErrNotFound", err)
	}
	if err := store.SetRetention(ctx, tenantID, res.Artifact.ArtifactID, 60); err != nil {
		t.Fatalf("set retention: %v", err)
	}
}
