package jobs

import (
	"context"

	s3adapter "github.com/synanton/content-service/internal/adapter/s3"
	"github.com/synanton/content-service/internal/security"
)

// AuditUpstream records one artifact-attributed audit event when err is an
// upstream-availability failure (timeout or open breaker, per the single
// s3.AuditReason taxonomy shared with the HTTP/gRPC surfaces). Nil
// auditor disables emission. Exactly one record per failed operation:
// callers invoke it once, on the failing path. Returns true when a
// record was emitted.
func AuditUpstream(ctx context.Context, auditor *security.Auditor, action, tenantID, artifactID string, err error) bool {
	if auditor == nil {
		return false
	}
	reason, ok := s3adapter.AuditReason(err)
	if !ok {
		return false
	}
	auditor.Record(ctx, security.AuditEvent{
		Actor:      "system",
		Action:     action,
		TenantID:   tenantID,
		ArtifactID: artifactID,
		Reason:     reason,
	})
	return true
}
