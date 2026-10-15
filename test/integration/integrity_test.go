//go:build integration

package integration

import (
	"errors"
	"testing"

	adapterpg "github.com/synanton/content-service/internal/adapter/postgres"
	"github.com/synanton/content-service/internal/domain"
	"github.com/synanton/content-service/internal/port"
)

// TestRealBackendCorruptionFailsClosed corrupts the object bytes behind
// a real artifact (simulating bit-rot or a tampered backend) and proves
// the read path fails closed: ErrIntegrityMismatch, never corrupt bytes.
// This is the CB-x store-level check against the production-shaped
// wiring; the fake-backed equivalent lives in internal/conformance.
func TestRealBackendCorruptionFailsClosed(t *testing.T) {
	tn := tenant()
	payload := make([]byte, 300*1024)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	res, err := store.Put(ctx, port.PutParams{
		TenantID: tn, Payload: payload,
		ContentType: "application/octet-stream", OwnerID: "owner-1",
		RetentionDays: 30,
	})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if res.Artifact.StorageTier != "s3" {
		t.Fatalf("tier = %s, want s3 (test must exercise the object backend)", res.Artifact.StorageTier)
	}
	key := adapterpg.BlobKey(tn, res.Artifact.Digest)
	tampered := make([]byte, 300*1024)
	for i := range tampered {
		tampered[i] = byte((i + 1) % 251)
	}
	if err := blobs.Put(ctx, key, tampered, "application/octet-stream"); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	data, _, err := store.Get(ctx, tn, res.Artifact.ArtifactID)
	if !errors.Is(err, domain.ErrIntegrityMismatch) {
		t.Fatalf("corrupt get err = %v, want ErrIntegrityMismatch", err)
	}
	if data != nil {
		t.Fatal("corrupt read served bytes alongside the error")
	}
	// Restore for suite hygiene: other tests share the backend.
	orig := make([]byte, 300*1024)
	for i := range orig {
		orig[i] = byte(i % 251)
	}
	if err := blobs.Put(ctx, key, orig, "application/octet-stream"); err != nil {
		t.Fatalf("restore: %v", err)
	}
}
