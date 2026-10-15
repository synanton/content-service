package http

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/synanton/content-service/internal/domain"
	"github.com/synanton/content-service/internal/port"
	"github.com/synanton/content-service/internal/security"
)

// timeoutStore fails Gets with a wrapped deadline: the shape a guarded
// S3 timeout takes after crossing the postgres store (%w preserved).
type timeoutStore struct {
	port.ContentStore
}

func (timeoutStore) Get(context.Context, string, string) ([]byte, *domain.Artifact, error) {
	return nil, nil, fmt.Errorf("fetch object tier: %w", context.DeadlineExceeded)
}

// TestGetTimeoutMaps503 proves slow-backend failures surface as
// retryable 503s, not 500s or partial content.
func TestGetTimeoutMaps503(t *testing.T) {
	h := NewHandler(timeoutStore{}, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/artifacts/some-id?tenant_id=acme", nil)
	rec := httptest.NewRecorder()
	h.Get(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (%s)", rec.Code, rec.Body.String())
	}
	var body errorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != "UPSTREAM_TIMEOUT" {
		t.Fatalf("code = %q, want UPSTREAM_TIMEOUT", body.Code)
	}
}

func captureAuditor() (*security.Auditor, *bytes.Buffer) {
	var buf bytes.Buffer
	return security.NewAuditorWithLogger(true, slog.New(slog.NewJSONHandler(&buf, nil))), &buf
}

func countAuditEvents(t *testing.T, buf *bytes.Buffer) []map[string]any {
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

// TestGetTimeoutEmitsExactlyOneAudit proves the D.3/C-5 evidence record:
// one artifact-attributed event=audit per failed operation — presence
// passes even with duplicates, exactly-once catches them.
func TestGetTimeoutEmitsExactlyOneAudit(t *testing.T) {
	auditor, buf := captureAuditor()
	h := NewHandler(timeoutStore{}, auditor)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/artifacts/artifact-9?tenant_id=acme", nil)
	rec := httptest.NewRecorder()
	h.Get(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", rec.Code)
	}
	events := countAuditEvents(t, buf)
	if len(events) != 1 {
		t.Fatalf("audit records = %d, want exactly 1:\n%s", len(events), buf.String())
	}
	got := events[0]
	if got["action"] != "get_failed" || got["reason"] != "UPSTREAM_TIMEOUT" {
		t.Errorf("action/reason = %v/%v", got["action"], got["reason"])
	}
	if got["tenant"] != "acme" || got["artifact"] != "artifact-9" {
		t.Errorf("tenant/artifact = %v/%v", got["tenant"], got["artifact"])
	}
}
