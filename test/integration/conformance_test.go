//go:build integration

package integration

import (
	"testing"

	"github.com/synanton/content-service/internal/conformance"
)

// TestCoreContractAgainstRealWiring runs the Design 1.26 Core behavioral
// suite against Postgres + MinIO. It reuses the TestMain store, so the same
// invariants validated against the fake in unit tests hold on real
// infrastructure.
func TestCoreContractAgainstRealWiring(t *testing.T) {
	conformance.Verify(t, store)
}
