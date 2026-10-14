package grpc

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v2/jwa"
	"github.com/lestrrat-go/jwx/v2/jwt"
	"github.com/synanton/security-provider/pkg/middleware"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	contentv1 "github.com/synanton/content-service/gen/content/v1"
	"github.com/synanton/content-service/internal/conformance"
)

var testKey = []byte("content-service-test-key-32-bytes!")

func mintToken(t *testing.T, tenant string, exp time.Time) string {
	t.Helper()
	tok := jwt.New()
	_ = tok.Set(jwt.IssuerKey, "synanton-security-provider")
	_ = tok.Set(jwt.SubjectKey, "service:ingestor")
	_ = tok.Set(jwt.AudienceKey, "synanton-internal")
	_ = tok.Set(jwt.ExpirationKey, exp)
	_ = tok.Set(jwt.IssuedAtKey, time.Now().Add(-time.Minute))
	_ = tok.Set("principal_type", "SERVICE")
	_ = tok.Set("tenant_id", tenant)
	_ = tok.Set("auth_method", "MTLS")
	_ = tok.Set("provider_id", "test-mtls")
	_ = tok.Set("assurance_level", "HIGH")
	_ = tok.Set("authenticated_at", time.Now().Add(-time.Minute).Format(time.RFC3339Nano))
	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.HS256, testKey))
	if err != nil {
		t.Fatal(err)
	}
	return string(signed)
}

func testClient(t *testing.T) contentv1.ContentServiceClient {
	t.Helper()
	v := &middleware.HMACVerifier{Key: testKey, ExpectedIssuer: "synanton-security-provider"}
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer(grpc.ChainUnaryInterceptor(middleware.UnaryServerInterceptor(v)))
	contentv1.RegisterContentServiceServer(srv, NewServer(conformance.NewFakeStore()))
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

func ctxWithToken(token string) context.Context {
	md := metadata.Pairs("authorization", "Bearer "+token)
	return metadata.NewOutgoingContext(context.Background(), md)
}

// TestGRPCAuthenticatedPutDerivesTenant proves D.1 on the gRPC surface:
// the tenant comes from the validated context.
func TestGRPCAuthenticatedPutDerivesTenant(t *testing.T) {
	client := testClient(t)
	token := mintToken(t, "tenant:acme", time.Now().Add(5*time.Minute))
	resp, err := client.Put(ctxWithToken(token), &contentv1.PutRequest{
		Payload: []byte("hello"), ContentType: "text/plain",
	})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if resp.GetArtifactId() == "" {
		t.Fatal("no artifact id")
	}
	get, err := client.Get(ctxWithToken(token), &contentv1.GetRequest{
		TenantId: "tenant:acme", ArtifactId: resp.GetArtifactId(),
	})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(get.GetPayload()) != "hello" {
		t.Fatalf("payload = %q", get.GetPayload())
	}
}

// TestGRPCOAuthenticatedNegative covers D.2 on the gRPC surface.
func TestGRPCAuthenticatedNegative(t *testing.T) {
	client := testClient(t)
	token := mintToken(t, "tenant:acme", time.Now().Add(5*time.Minute))
	if _, err := client.Put(context.Background(), &contentv1.PutRequest{Payload: []byte("x")}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("missing token: code = %v", status.Code(err))
	}
	if _, err := client.Put(ctxWithToken(token), &contentv1.PutRequest{
		TenantId: "tenant:evil", Payload: []byte("x"),
	}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("forged tenant: code = %v", status.Code(err))
	}
	md := metadata.Pairs("authorization", "Bearer "+token, "x-tenant-id", "tenant:evil")
	if _, err := client.Put(metadata.NewOutgoingContext(context.Background(), md), &contentv1.PutRequest{
		Payload: []byte("x"),
	}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("conflicting metadata tenant: code = %v", status.Code(err))
	}
}
