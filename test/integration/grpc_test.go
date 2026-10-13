//go:build integration

package integration

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
	grpchandler "github.com/synanton/content-service/internal/adapter/grpc"
)

func grpcClient(t *testing.T) contentv1.ContentServiceClient {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	t.Cleanup(func() { _ = lis.Close() })
	srv := grpc.NewServer()
	t.Cleanup(srv.GracefulStop)
	contentv1.RegisterContentServiceServer(srv, grpchandler.NewServer(store))
	go func() { _ = srv.Serve(lis) }()
	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial bufnet: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return contentv1.NewContentServiceClient(conn)
}

func TestGrpcPutGetRoundTrip(t *testing.T) {
	client := grpcClient(t)
	tenantID := tenant()
	put, err := client.Put(ctx, &contentv1.PutRequest{
		TenantId:    tenantID,
		Payload:     []byte("hello grpc"),
		ContentType: "text/plain",
		OwnerId:     "owner-1",
	})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if put.GetDedupHit() {
		t.Fatal("first put must not be a dedup hit")
	}
	if put.GetReference() == "" || put.GetArtifactId() == "" || put.GetDigest() == "" {
		t.Fatal("put response must carry reference, artifact id, and digest")
	}
	get, err := client.Get(ctx, &contentv1.GetRequest{
		TenantId:   tenantID,
		ArtifactId: put.GetArtifactId(),
	})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(get.GetPayload()) != "hello grpc" {
		t.Fatal("payload mismatch after gRPC round trip")
	}
	if get.GetDigest() != put.GetDigest() {
		t.Fatal("digest mismatch after gRPC round trip")
	}
}

func TestGrpcDedupSameTenant(t *testing.T) {
	client := grpcClient(t)
	tenantID := tenant()
	payload := []byte("grpc dedup me")
	first, err := client.Put(ctx, &contentv1.PutRequest{TenantId: tenantID, Payload: payload})
	if err != nil {
		t.Fatalf("first put: %v", err)
	}
	second, err := client.Put(ctx, &contentv1.PutRequest{TenantId: tenantID, Payload: payload})
	if err != nil {
		t.Fatalf("second put: %v", err)
	}
	if !second.GetDedupHit() {
		t.Fatal("second put of identical bytes must be a dedup hit")
	}
	if second.GetArtifactId() != first.GetArtifactId() {
		t.Fatal("dedup hit must return the same artifact id")
	}
}

func TestGrpcCrossTenantIsIndistinguishable(t *testing.T) {
	client := grpcClient(t)
	put, err := client.Put(ctx, &contentv1.PutRequest{
		TenantId: tenant(), Payload: []byte("grpc secret"),
	})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	_, err = client.Get(ctx, &contentv1.GetRequest{
		TenantId: tenant(), ArtifactId: put.GetArtifactId(),
	})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("cross-tenant get code = %v, want NotFound", status.Code(err))
	}
	head, err := client.Head(ctx, &contentv1.HeadRequest{
		TenantId: tenant(), ArtifactId: put.GetArtifactId(),
	})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("cross-tenant head code = %v, want NotFound", status.Code(err))
	}
	_ = head
}

func TestGrpcHeadAndCapabilities(t *testing.T) {
	client := grpcClient(t)
	tenantID := tenant()
	put, err := client.Put(ctx, &contentv1.PutRequest{TenantId: tenantID, Payload: []byte("head me")})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	head, err := client.Head(ctx, &contentv1.HeadRequest{TenantId: tenantID, ArtifactId: put.GetArtifactId()})
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	if !head.GetExists() {
		t.Fatal("head must report an existing artifact")
	}
	caps, err := client.Capabilities(ctx, &contentv1.CapabilitiesRequest{})
	if err != nil {
		t.Fatalf("capabilities: %v", err)
	}
	if caps.GetConformanceLevel() != "CORE" {
		t.Fatalf("conformance level = %s, want CORE", caps.GetConformanceLevel())
	}
	if len(caps.GetStorageTiers()) == 0 {
		t.Fatal("capabilities must advertise storage tiers")
	}
}

func TestGrpcPutRejectsEmptyTenant(t *testing.T) {
	client := grpcClient(t)
	_, err := client.Put(ctx, &contentv1.PutRequest{Payload: []byte("x")})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("empty tenant code = %v, want InvalidArgument", status.Code(err))
	}
}

func TestGrpcLargePayloadUsesObjectTier(t *testing.T) {
	client := grpcClient(t)
	tenantID := tenant()
	payload := largePayload('g', 300*1024)
	put, err := client.Put(ctx, &contentv1.PutRequest{
		TenantId: tenantID, Payload: payload, OwnerId: "owner-1",
	})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if put.GetStorageTier() != "s3" {
		t.Fatalf("tier = %s, want s3", put.GetStorageTier())
	}
	get, err := client.Get(ctx, &contentv1.GetRequest{
		TenantId: tenantID, ArtifactId: put.GetArtifactId(),
	})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(get.GetPayload()) != string(payload) {
		t.Fatal("payload mismatch after gRPC object-tier round trip")
	}
}
