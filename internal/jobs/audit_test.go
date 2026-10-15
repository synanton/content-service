package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/synanton/content-service/internal/security"
)

func captureAuditor() (*security.Auditor, *bytes.Buffer) {
	var buf bytes.Buffer
	return security.NewAuditorWithLogger(true, slog.New(slog.NewJSONHandler(&buf, nil))), &buf
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

// TestAuditUpstreamRecordsOnce pins the job-side contract: an upstream
// failure yields exactly one artifact-attributed record with the shared
// reason taxonomy; anything else (including nil auditor) yields none.
func TestAuditUpstreamRecordsOnce(t *testing.T) {
	ctx := context.Background()
	upstream := fmt.Errorf("sweep: list objects: %w", context.DeadlineExceeded)

	auditor, buf := captureAuditor()
	if !AuditUpstream(ctx, auditor, "sweep_failed", "tenant:acme", "artifact-1", upstream) {
		t.Fatal("upstream failure not recorded")
	}
	if AuditUpstream(ctx, auditor, "sweep_failed", "tenant:acme", "artifact-1", upstream) != true {
		t.Fatal("second failure not recorded")
	}
	events := auditEvents(t, buf)
	if len(events) != 2 {
		t.Fatalf("records = %d, want 2 (one per call):\n%s", len(events), buf.String())
	}
	for _, ev := range events {
		if ev["action"] != "sweep_failed" || ev["reason"] != "UPSTREAM_TIMEOUT" {
			t.Errorf("action/reason = %v/%v", ev["action"], ev["reason"])
		}
		if ev["tenant"] != "tenant:acme" || ev["artifact"] != "artifact-1" {
			t.Errorf("tenant/artifact = %v/%v", ev["tenant"], ev["artifact"])
		}
	}

	if AuditUpstream(ctx, auditor, "sweep_failed", "t", "a", errors.New("caller bug")) {
		t.Error("non-upstream failure recorded")
	}
	if AuditUpstream(ctx, nil, "sweep_failed", "t", "a", upstream) {
		t.Error("nil auditor recorded")
	}
	if AuditUpstream(ctx, auditor, "sweep_failed", "t", "a", nil) {
		t.Error("nil error recorded")
	}
}
