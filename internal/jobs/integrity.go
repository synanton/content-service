package jobs

import (
	"context"
	"errors"
	"fmt"

	"github.com/synanton/content-service/internal/domain"
	"github.com/synanton/content-service/internal/observability"
	"github.com/synanton/content-service/internal/port"
	"github.com/synanton/content-service/internal/security"
)

// Integrity re-reads payloads and verifies digests (design §5: integrity job).
// Reads go through port.ContentStore.Get, which fails closed on mismatch, so
// this job detects bit-rot, truncation, and storage-layer aliasing bugs alike.
// Findings are audited; there is no auto-quarantine yet (needs a schema
// quarantine flag — recorded as future work, never silent).
type Integrity struct {
	Store      LifecycleStore
	Content    port.ContentStore
	Batch      int
	SampleRate float64
	ShardIndex int
	ShardCount int
	Auditor    *security.Auditor
}

// Result summarizes one sweep.
type Result struct {
	Checked int
	Failed  int
}

func (in *Integrity) Sweep(ctx context.Context) (Result, error) {
	var result Result
	tenants, err := in.Store.ListTenants(ctx)
	if err != nil {
		return result, fmt.Errorf("integrity: list tenants: %w", err)
	}
	for _, tenant := range tenants {
		if !InShard(tenant, in.ShardIndex, in.ShardCount) {
			continue
		}
		ids, err := in.Store.ListArtifactIDs(ctx, tenant, in.Batch, in.SampleRate)
		if err != nil {
			return result, fmt.Errorf("integrity: list artifacts for %s: %w", tenant, err)
		}
		for _, id := range ids {
			result.Checked++
			observability.IntegrityChecked.WithLabelValues(tenant).Inc()
			failed, err := in.verifyOne(ctx, tenant, id)
			if err != nil {
				return result, err
			}
			if failed {
				result.Failed++
			}
		}
	}
	return result, nil
}

// verifyOne returns failed=true when the artifact is corrupt (audited,
// counted); infrastructure errors abort the sweep.
func (in *Integrity) verifyOne(ctx context.Context, tenant, artifact string) (failed bool, err error) {
	_, _, err = in.Content.Get(ctx, tenant, artifact)
	if err == nil {
		return false, nil
	}
	if errors.Is(err, domain.ErrIntegrityMismatch) {
		if recErr := in.Store.RecordIntegrityFailure(ctx, tenant, artifact, err.Error()); recErr != nil {
			return false, fmt.Errorf("integrity: record failure %s/%s: %w", tenant, artifact, recErr)
		}
		observability.IntegrityFailures.WithLabelValues(tenant).Inc()
		return true, nil
	}
	if errors.Is(err, domain.ErrNotFound) {
		return false, nil
	}
	AuditUpstream(ctx, in.Auditor, "integrity_failed", tenant, artifact, err)
	return false, fmt.Errorf("integrity: verify %s/%s: %w", tenant, artifact, err)
}
