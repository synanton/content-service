package security

import (
	"context"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v2/jwa"
	"github.com/lestrrat-go/jwx/v2/jwt"
	"github.com/synanton/security-provider/pkg/middleware"

	"github.com/synanton/content-service/internal/config"
)

var testKey = []byte("content-service-test-key-32-bytes!")

func mintToken(t *testing.T, key []byte, issuer, tenant, principal string, exp time.Time) string {
	t.Helper()
	tok := jwt.New()
	_ = tok.Set(jwt.IssuerKey, issuer)
	_ = tok.Set(jwt.SubjectKey, principal)
	_ = tok.Set(jwt.AudienceKey, "synanton-internal")
	_ = tok.Set(jwt.ExpirationKey, exp)
	_ = tok.Set(jwt.IssuedAtKey, time.Now().Add(-time.Minute))
	_ = tok.Set("principal_type", "SERVICE")
	_ = tok.Set("tenant_id", tenant)
	_ = tok.Set("auth_method", "MTLS")
	_ = tok.Set("provider_id", "test-mtls")
	_ = tok.Set("assurance_level", "HIGH")
	_ = tok.Set("authenticated_at", time.Now().Add(-time.Minute).Format(time.RFC3339Nano))
	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.HS256, key))
	if err != nil {
		t.Fatal(err)
	}
	return string(signed)
}

func contextWithVerifier(t *testing.T, v middleware.Verifier, token string) context.Context {
	t.Helper()
	sc, err := v.Verify(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	return middleware.WithSecurityContext(context.Background(), sc)
}

func TestNewVerifierDisabled(t *testing.T) {
	v, err := NewVerifier(config.AuthConfig{Enabled: false})
	if err != nil || v != nil {
		t.Fatalf("disabled auth must yield (nil, nil), got %v %v", v, err)
	}
}

func TestNewVerifierRequiresKey(t *testing.T) {
	if _, err := NewVerifier(config.AuthConfig{Enabled: true, HMACKey: "short"}); err == nil {
		t.Fatal("short HMAC key accepted")
	}
	v, err := NewVerifier(config.AuthConfig{Enabled: true, HMACKey: string(testKey), Issuer: "synanton-security-provider"})
	if err != nil || v == nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

func TestResolveTenant(t *testing.T) {
	v, err := NewVerifier(config.AuthConfig{Enabled: true, HMACKey: string(testKey), Issuer: "synanton-security-provider"})
	if err != nil {
		t.Fatal(err)
	}
	token := mintToken(t, testKey, "synanton-security-provider", "tenant:acme", "service:ingestor", time.Now().Add(5*time.Minute))
	authCtx := contextWithVerifier(t, v, token)

	tenant, err := ResolveTenant(authCtx, "tenant:acme", "")
	if err != nil || tenant != "tenant:acme" {
		t.Fatalf("ResolveTenant = %q, %v", tenant, err)
	}
	if _, err := ResolveTenant(authCtx, "tenant:evil"); err == nil {
		t.Fatal("forged hint accepted")
	}
	if tenant, err := ResolveTenant(context.Background(), "tenant:acme"); err != nil || tenant != "" {
		t.Fatalf("no context must yield legacy path, got %q %v", tenant, err)
	}
	if _, err := TenantFromContext(context.Background()); err == nil {
		t.Fatal("missing context must error")
	}
}
