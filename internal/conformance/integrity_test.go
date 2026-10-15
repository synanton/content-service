package conformance

import (
	"context"
	"errors"
	"testing"

	"github.com/synanton/content-service/internal/domain"
	"github.com/synanton/content-service/internal/port"
)

// CB-x: integrity coverage guarantee. The guarantee, precisely stated:
//
//   - Read path: FULL deterministic verification. Every Get recomputes
//     SHA-256 over the fetched bytes and fails closed on mismatch.
//   - Background sweep: SAMPLED (default rate 0.01; exact math in
//     docs/benchmarks.md "Integrity coverage math"). Sampling controls
//     cost and must never be presented as coverage.
//   - Quarantine: ABSENT (see TestIntegrityQuarantineNotImplemented).
//
// These checks pin the guarantee at the store contract. Surface mapping
// (HTTP 409 / gRPC DataLoss) is asserted in the adapter packages, where
// the status translation lives.
func TestCorruptBytesNeverServed(t *testing.T) {
	ctx := context.Background()
	store := NewFakeStore()
	res, err := store.Put(ctx, port.PutParams{
		TenantID: "tenant:acme", Payload: []byte("integrity probe payload"),
		ContentType: "text/plain", OwnerID: "owner-1", RetentionDays: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	store.CorruptPayload("tenant:acme", res.Artifact.ArtifactID)
	data, _, err := store.Get(ctx, "tenant:acme", res.Artifact.ArtifactID)
	if !errors.Is(err, domain.ErrIntegrityMismatch) {
		t.Fatalf("corrupt get err = %v, want ErrIntegrityMismatch", err)
	}
	if data != nil {
		t.Fatal("corrupt read served bytes alongside the error")
	}
}

// TestIntegrityQuarantineNotImplemented documents the known limitation:
// on mismatch the artifact fails closed per-read, stays listed (Exists
// true), and no quarantine state exists anywhere in the contract. When
// quarantine lands, this check flips to assert the transition instead
// of its absence.
func TestIntegrityQuarantineNotImplemented(t *testing.T) {
	ctx := context.Background()
	store := NewFakeStore()
	res, err := store.Put(ctx, port.PutParams{
		TenantID: "tenant:acme", Payload: []byte("quarantine probe"),
		ContentType: "text/plain", OwnerID: "owner-1", RetentionDays: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	store.CorruptPayload("tenant:acme", res.Artifact.ArtifactID)
	if _, _, err := store.Get(ctx, "tenant:acme", res.Artifact.ArtifactID); !errors.Is(err, domain.ErrIntegrityMismatch) {
		t.Fatalf("corrupt get err = %v, want ErrIntegrityMismatch", err)
	}
	// No quarantine transition: the artifact is still listed, and every
	// subsequent read fails closed independently. There is no quarantine
	// state to assert because the contract has no quarantine concept —
	// that absence is exactly what this check documents.
	if ok, err := store.Exists(ctx, "tenant:acme", res.Artifact.ArtifactID); err != nil || !ok {
		t.Fatalf("corrupt artifact unlisted: exists = %v, %v", ok, err)
	}
	if _, _, err := store.Get(ctx, "tenant:acme", res.Artifact.ArtifactID); !errors.Is(err, domain.ErrIntegrityMismatch) {
		t.Fatalf("second corrupt get err = %v, want ErrIntegrityMismatch", err)
	}
}
