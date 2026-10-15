//go:build integration

// D.1/D.2 evidence: the full content-service stack (real Postgres + MinIO
// via testcontainers) behind the Security Provider middleware. Tenant is
// derived from the validated context; hints are cross-checked.
package integration

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
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
	grpchandler "github.com/synanton/content-service/internal/adapter/grpc"
	httphandler "github.com/synanton/content-service/internal/adapter/http"
)

var authKey = []byte("content-service-auth-test-key-32B!")

func mintAuthToken(t *testing.T, tenant string, exp time.Time) string {
	t.Helper()
	tok := jwt.New()
	_ = tok.Set(jwt.IssuerKey, "synanton-security-provider")
	_ = tok.Set(jwt.SubjectKey, "service:commitix")
	_ = tok.Set(jwt.AudienceKey, "synanton-internal")
	_ = tok.Set(jwt.ExpirationKey, exp)
	_ = tok.Set(jwt.IssuedAtKey, time.Now().Add(-time.Minute))
	_ = tok.Set("principal_type", "SERVICE")
	_ = tok.Set("tenant_id", tenant)
	_ = tok.Set("auth_method", "MTLS")
	_ = tok.Set("provider_id", "synanton-workload-ca")
	_ = tok.Set("assurance_level", "HIGH")
	_ = tok.Set("authenticated_at", time.Now().Add(-time.Minute).Format(time.RFC3339Nano))
	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.HS256, authKey))
	if err != nil {
		t.Fatal(err)
	}
	return string(signed)
}

func authedMux() http.Handler {
	v := &middleware.HMACVerifier{Key: authKey, ExpectedIssuer: "synanton-security-provider"}
	h := httphandler.NewHandler(store, nil)
	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/artifacts", middleware.RequireAuth(v)(http.HandlerFunc(h.Put)))
	mux.Handle("GET /api/v1/artifacts/", middleware.RequireAuth(v)(http.HandlerFunc(h.Get)))
	mux.Handle("HEAD /api/v1/artifacts/", middleware.RequireAuth(v)(http.HandlerFunc(h.Head)))
	return mux
}

func authPut(t *testing.T, mux http.Handler, headers map[string]string, bodyTenant string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]string{
		"tenant_id":    bodyTenant,
		"payload":      base64.StdEncoding.EncodeToString([]byte("auth payload")),
		"content_type": "text/plain",
		"owner_id":     "auth-test",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/artifacts", bytes.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestAuthPutDerivesTenant is the D.1 handshake on the real stack: an
// authenticated request validates via the middleware and the write lands
// under the context tenant.
func TestAuthPutDerivesTenant(t *testing.T) {
	mux := authedMux()
	token := mintAuthToken(t, tenant(), time.Now().Add(5*time.Minute))
	rec := authPut(t, mux, map[string]string{"Authorization": "Bearer " + token}, "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
	}
	var out struct {
		ArtifactID string `json:"artifact_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.ArtifactID == "" {
		t.Fatalf("put response: %s / %v", rec.Body.String(), err)
	}
	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/artifacts/"+out.ArtifactID, nil)
	getReq.Header.Set("Authorization", "Bearer "+token)
	getRec := httptest.NewRecorder()
	mux.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("get: status = %d (%s)", getRec.Code, getRec.Body.String())
	}
}

// TestAuthNegative is the D.2 battery on the real stack.
func TestAuthNegative(t *testing.T) {
	mux := authedMux()
	token := mintAuthToken(t, tenant(), time.Now().Add(5*time.Minute))
	expired := mintAuthToken(t, tenant(), time.Now().Add(-time.Hour))
	cases := []struct {
		name    string
		headers map[string]string
		body    string
		url     string
		want    int
	}{
		{"missing token", nil, "", "/api/v1/artifacts", http.StatusUnauthorized},
		{"bad token", map[string]string{"Authorization": "Bearer garbage"}, "", "/api/v1/artifacts", http.StatusUnauthorized},
		{"expired token", map[string]string{"Authorization": "Bearer " + expired}, "", "/api/v1/artifacts", http.StatusUnauthorized},
		{"forged header", map[string]string{"Authorization": "Bearer " + token, "X-Tenant-ID": "tenant:evil"}, "", "/api/v1/artifacts", http.StatusForbidden},
		{"forged query", map[string]string{"Authorization": "Bearer " + token}, "", "/api/v1/artifacts", http.StatusForbidden},
		{"forged body", map[string]string{"Authorization": "Bearer " + token}, "tenant:evil", "/api/v1/artifacts", http.StatusForbidden},
		{"unsigned user", map[string]string{"Authorization": "Bearer " + token, "X-User-ID": "user:mallory"}, "", "/api/v1/artifacts", http.StatusForbidden},
	}
	for _, tc := range cases {
		url := tc.url
		if tc.name == "forged query" {
			url += "?tenant_id=tenant:evil"
		}
		body, _ := json.Marshal(map[string]string{
			"tenant_id": tc.body, "payload": base64.StdEncoding.EncodeToString([]byte("x")),
		})
		req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(body))
		for k, v := range tc.headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("%s: status = %d, want %d (%s)", tc.name, rec.Code, tc.want, rec.Body.String())
		}
	}
}

// TestAuthCrossTenantDenied proves tenant A content is unreachable with a
// tenant B context, and the failure is indistinguishable from missing.
func TestAuthCrossTenantDenied(t *testing.T) {
	mux := authedMux()
	tokenA := mintAuthToken(t, tenant(), time.Now().Add(5*time.Minute))
	rec := authPut(t, mux, map[string]string{"Authorization": "Bearer " + tokenA}, "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup put: %d", rec.Code)
	}
	var out struct {
		ArtifactID string `json:"artifact_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	tokenB := mintAuthToken(t, tenant()+"-other", time.Now().Add(5*time.Minute))
	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/artifacts/"+out.ArtifactID, nil)
	getReq.Header.Set("Authorization", "Bearer "+tokenB)
	getRec := httptest.NewRecorder()
	mux.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant read: status = %d, want 404", getRec.Code)
	}
}

// TestAuthGRPCEnforced proves D.1/D.2 on the gRPC surface against the
// real store.
func TestAuthGRPCEnforced(t *testing.T) {
	v := &middleware.HMACVerifier{Key: authKey, ExpectedIssuer: "synanton-security-provider"}
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer(grpc.ChainUnaryInterceptor(middleware.UnaryServerInterceptor(v)))
	contentv1.RegisterContentServiceServer(srv, grpchandler.NewServer(store, nil))
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

	ten := tenant()
	token := mintAuthToken(t, ten, time.Now().Add(5*time.Minute))
	mdCtx := metadata.NewOutgoingContext(context.Background(),
		metadata.Pairs("authorization", "Bearer "+token))
	put, err := client.Put(mdCtx, &contentv1.PutRequest{Payload: []byte("grpc auth"), ContentType: "text/plain"})
	if err != nil {
		t.Fatalf("authenticated put: %v", err)
	}
	if _, err := client.Get(mdCtx, &contentv1.GetRequest{
		TenantId: ten, ArtifactId: put.GetArtifactId(),
	}); err != nil {
		t.Fatalf("authenticated get: %v", err)
	}
	if _, err := client.Put(mdCtx, &contentv1.PutRequest{
		TenantId: "tenant:evil", Payload: []byte("x"),
	}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("forged tenant: code = %v", status.Code(err))
	}
	if _, err := client.Put(context.Background(), &contentv1.PutRequest{
		Payload: []byte("x"),
	}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("missing token: code = %v", status.Code(err))
	}
}
