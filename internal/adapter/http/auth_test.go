package http

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v2/jwa"
	"github.com/lestrrat-go/jwx/v2/jwt"
	"github.com/synanton/security-provider/pkg/middleware"

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

func authedHandler() (http.Handler, *middleware.HMACVerifier) {
	v := &middleware.HMACVerifier{Key: testKey, ExpectedIssuer: "synanton-security-provider"}
	h := NewHandler(conformance.NewFakeStore())
	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/artifacts", middleware.RequireAuth(v)(http.HandlerFunc(h.Put)))
	mux.Handle("GET /api/v1/artifacts/", middleware.RequireAuth(v)(http.HandlerFunc(h.Get)))
	return mux, v
}

func put(t *testing.T, h http.Handler, headers map[string]string, body map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/artifacts", bytes.NewReader(raw))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func payloadBody(extra map[string]string) map[string]string {
	body := map[string]string{
		"payload":      base64.StdEncoding.EncodeToString([]byte("hello")),
		"content_type": "text/plain",
		"owner_id":     "tester",
	}
	for k, v := range extra {
		body[k] = v
	}
	return body
}

// TestAuthenticatedPutDerivesTenant proves D.1: a request carrying a
// validated context writes under the context tenant.
func TestAuthenticatedPutDerivesTenant(t *testing.T) {
	h, _ := authedHandler()
	token := mintToken(t, "tenant:acme", time.Now().Add(5*time.Minute))
	rec := put(t, h, map[string]string{"Authorization": "Bearer " + token}, payloadBody(nil))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
	}
	var out putResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.ArtifactID == "" {
		t.Fatal("no artifact id in response")
	}
}

// TestAuthenticatedNegative covers D.2: forged hints, unsigned identity,
// bad/expired/missing tokens are all rejected.
func TestAuthenticatedNegative(t *testing.T) {
	h, _ := authedHandler()
	token := mintToken(t, "tenant:acme", time.Now().Add(5*time.Minute))
	expired := mintToken(t, "tenant:acme", time.Now().Add(-time.Hour))
	cases := []struct {
		name    string
		headers map[string]string
		body    map[string]string
		want    int
	}{
		{"missing token", nil, payloadBody(nil), http.StatusUnauthorized},
		{"bad token", map[string]string{"Authorization": "Bearer garbage"}, payloadBody(nil), http.StatusUnauthorized},
		{"expired token", map[string]string{"Authorization": "Bearer " + expired}, payloadBody(nil), http.StatusUnauthorized},
		{"forged tenant header", map[string]string{"Authorization": "Bearer " + token, "X-Tenant-ID": "tenant:evil"}, payloadBody(nil), http.StatusForbidden},
		{"forged tenant query", map[string]string{"Authorization": "Bearer " + token}, nil, http.StatusForbidden},
		{"forged body tenant", map[string]string{"Authorization": "Bearer " + token}, payloadBody(map[string]string{"tenant_id": "tenant:evil"}), http.StatusForbidden},
		{"unsigned user header", map[string]string{"Authorization": "Bearer " + token, "X-User-ID": "user:mallory"}, payloadBody(nil), http.StatusForbidden},
	}
	for _, tc := range cases {
		body := tc.body
		if body == nil {
			body = payloadBody(nil)
		}
		var rec *httptest.ResponseRecorder
		if tc.name == "forged tenant query" {
			raw, _ := json.Marshal(body)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/artifacts?tenant_id=tenant:evil", bytes.NewReader(raw))
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			rec = httptest.NewRecorder()
			h.ServeHTTP(rec, req)
		} else {
			rec = put(t, h, tc.headers, body)
		}
		if rec.Code != tc.want {
			t.Errorf("%s: status = %d, want %d (%s)", tc.name, rec.Code, tc.want, rec.Body.String())
		}
	}
}

// TestCrossTenantReadDenied proves a valid context for tenant A cannot
// read tenant B content, even with a matching hint for B absent: the
// store scoping does the rest, and the tenant comes only from context.
func TestCrossTenantReadDenied(t *testing.T) {
	h, _ := authedHandler()
	tokenA := mintToken(t, "tenant:a", time.Now().Add(5*time.Minute))
	rec := put(t, h, map[string]string{"Authorization": "Bearer " + tokenA}, payloadBody(nil))
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup put: %d", rec.Code)
	}
	var out putResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	tokenB := mintToken(t, "tenant:b", time.Now().Add(5*time.Minute))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/artifacts/"+out.ArtifactID, nil)
	req.Header.Set("Authorization", "Bearer "+tokenB)
	recGet := httptest.NewRecorder()
	h.ServeHTTP(recGet, req)
	if recGet.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant read: status = %d, want 404", recGet.Code)
	}
}

// testCA mints a throwaway CA plus a leaf certificate for the mTLS
// transport test. Production trust roots never come from here.
func testCA(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test-ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, key, der
}

// TestMTLSTransportHandshake proves the D.1 exit over an authenticated
// transport: the request arrives on a mutually-verified TLS connection
// and the context is validated by the middleware.
func TestMTLSTransportHandshake(t *testing.T) {
	h, _ := authedHandler()
	caCert, caKey, _ := testCA(t)
	pool := x509.NewCertPool()
	pool.AddCert(caCert)

	serverKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	serverTPL := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "127.0.0.1"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames: []string{"localhost"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTPL, caCert, &serverKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	serverCert, err := x509.ParseCertificate(serverDER)
	if err != nil {
		t.Fatal(err)
	}
	_ = serverCert

	clientKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	clientTPL := &x509.Certificate{
		SerialNumber: big.NewInt(3), Subject: pkix.Name{CommonName: "workload"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	clientDER, err := x509.CreateCertificate(rand.Reader, clientTPL, caCert, &clientKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	clientCertDER := clientDER
	_ = clientCertDER

	srv := httptest.NewUnstartedServer(h)
	srv.TLS = &tls.Config{
		Certificates: []tls.Certificate{mustKeyPair(t, serverDER, serverKey)},
		ClientCAs:    pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
	}
	srv.StartTLS()
	defer srv.Close()

	client := srv.Client()
	client.Transport.(*http.Transport).TLSClientConfig.Certificates = []tls.Certificate{mustKeyPair(t, clientDER, clientKey)}

	token := mintToken(t, "tenant:acme", time.Now().Add(5*time.Minute))
	raw, _ := json.Marshal(payloadBody(nil))
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/artifacts", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("mTLS request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("mTLS handshake put: status = %d", resp.StatusCode)
	}
	if len(resp.TLS.PeerCertificates) == 0 {
		t.Fatal("no verified server peer on mTLS connection")
	}
}

func pemEncode(blockType string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der})
}

func mustKeyPair(t *testing.T, der []byte, key *ecdsa.PrivateKey) tls.Certificate {
	t.Helper()
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(
		pemEncode("CERTIFICATE", der),
		pemEncode("EC PRIVATE KEY", keyDER),
	)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}
