package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/synanton/content-service/internal/domain"
	"github.com/synanton/content-service/internal/port"
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
	h := NewHandler(timeoutStore{})
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
