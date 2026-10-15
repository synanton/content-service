package grpc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	contentv1 "github.com/synanton/content-service/gen/content/v1"
	"github.com/synanton/content-service/internal/domain"
	"github.com/synanton/content-service/internal/port"
	"github.com/synanton/content-service/internal/security"
)

// timeoutStore fails reads with a wrapped deadline: the shape a guarded
// S3 timeout takes after crossing the postgres store (%w preserved).
type timeoutStore struct {
	port.ContentStore
}

func (timeoutStore) Get(context.Context, string, string) ([]byte, *domain.Artifact, error) {
	return nil, nil, fmt.Errorf("fetch object tier: %w", context.DeadlineExceeded)
}

func clientWithAuditor(t *testing.T, auditor *security.Auditor) contentv1.ContentServiceClient {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	contentv1.RegisterContentServiceServer(srv, NewServer(timeoutStore{}, auditor))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return contentv1.NewContentServiceClient(conn)
}

// TestGetTimeoutUnavailableAndAudited proves the gRPC half of the D.3
// evidence: Unavailable (not Internal) plus exactly one artifact-
// attributed event=audit per failed operation.
func TestGetTimeoutUnavailableAndAudited(t *testing.T) {
	var buf bytes.Buffer
	auditor := security.NewAuditorWithLogger(true, slog.New(slog.NewJSONHandler(&buf, nil)))
	client := clientWithAuditor(t, auditor)
	_, err := client.Get(context.Background(), &contentv1.GetRequest{
		TenantId: "tenant:acme", ArtifactId: "artifact-9",
	})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("code = %v, want Unavailable", status.Code(err))
	}
	count := 0
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var decoded map[string]any
		if err := json.Unmarshal([]byte(line), &decoded); err != nil {
			t.Fatalf("audit output is not JSON: %v", err)
		}
		if decoded["event"] != "audit" {
			continue
		}
		count++
		if decoded["action"] != "get_failed" || decoded["reason"] != "UPSTREAM_TIMEOUT" {
			t.Errorf("action/reason = %v/%v", decoded["action"], decoded["reason"])
		}
		if decoded["tenant"] != "tenant:acme" || decoded["artifact"] != "artifact-9" {
			t.Errorf("tenant/artifact = %v/%v", decoded["tenant"], decoded["artifact"])
		}
	}
	if count != 1 {
		t.Fatalf("audit records = %d, want exactly 1:\n%s", count, buf.String())
	}
}
