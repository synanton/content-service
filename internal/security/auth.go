package security

import (
	"context"
	"fmt"

	"github.com/synanton/security-provider/pkg/middleware"

	"github.com/synanton/content-service/internal/config"
)

// NewVerifier builds the Security Provider context verifier from
// configuration. It returns (nil, nil) when authentication is disabled,
// so call sites keep serving exactly as before.
func NewVerifier(auth config.AuthConfig) (middleware.Verifier, error) {
	if !auth.Enabled {
		return nil, nil
	}
	if len(auth.HMACKey) < 32 {
		return nil, fmt.Errorf("auth HMAC key must be at least 32 bytes")
	}
	return &middleware.HMACVerifier{
		Key:            []byte(auth.HMACKey),
		ExpectedIssuer: auth.Issuer,
	}, nil
}

// TenantFromContext returns the tenant derived from the validated
// security context. It is the only legitimate tenant source when
// authentication is enabled; ErrNoTenant means no validated context is
// present (middleware disabled or bypassed).
func TenantFromContext(ctx context.Context) (string, error) {
	tenant, err := middleware.TenantIDFromContext(ctx)
	if err != nil {
		return "", ErrNoTenant
	}
	return tenant, nil
}

// ResolveTenant derives the tenant from the validated context and
// cross-checks every caller-supplied hint (header, query, body field, or
// message field). An absent hint is fine; a mismatched hint is
// ErrTenantMismatch. Without a validated context it returns ("", nil) so
// legacy unauthenticated resolution still applies.
func ResolveTenant(ctx context.Context, hints ...string) (string, error) {
	tenant, err := TenantFromContext(ctx)
	if err != nil {
		return "", nil
	}
	for _, hint := range hints {
		if hint == "" {
			continue
		}
		if hint != tenant {
			return "", fmt.Errorf("%w: hint %q != context tenant %q", ErrTenantMismatch, hint, tenant)
		}
	}
	return tenant, nil
}
