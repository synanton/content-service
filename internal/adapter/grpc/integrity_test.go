package grpc

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	contentv1 "github.com/synanton/content-service/gen/content/v1"
	"github.com/synanton/content-service/internal/conformance"
	"github.com/synanton/content-service/internal/port"
)

// TestCorruptPayloadMapsDataLoss proves the gRPC half of the CB-x
// guarantee: verification failure surfaces as DataLoss, never Ok with
// corrupt bytes and never a bare Internal.
func TestCorruptPayloadMapsDataLoss(t *testing.T) {
	store := conformance.NewFakeStore()
	put, err := store.Put(context.Background(), port.PutParams{
		TenantID: "acme", Payload: []byte("grpc integrity probe"),
		ContentType: "text/plain", OwnerID: "t", RetentionDays: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	store.CorruptPayload("acme", put.Artifact.ArtifactID)

	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	contentv1.RegisterContentServiceServer(srv, NewServer(store, nil))
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
	client := contentv1.NewContentServiceClient(conn)

	_, err = client.Get(context.Background(), &contentv1.GetRequest{
		TenantId: "acme", ArtifactId: put.Artifact.ArtifactID,
	})
	if status.Code(err) != codes.DataLoss {
		t.Fatalf("code = %v, want DataLoss", status.Code(err))
	}
}
