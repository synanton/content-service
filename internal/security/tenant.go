package security

import (
	"context"
	"errors"
	"fmt"
)

type contextKey int

const (
	tenantIDKey contextKey = iota
)

var ErrNoTenant = errors.New("no tenant in context")
var ErrTenantMismatch = errors.New("tenant mismatch")

func WithTenantID(ctx context.Context, tenantID string) context.Context {
	return context.WithValue(ctx, tenantIDKey, tenantID)
}

func TenantIDFromContext(ctx context.Context) (string, error) {
	tenantID, ok := ctx.Value(tenantIDKey).(string)
	if !ok || tenantID == "" {
		return "", ErrNoTenant
	}
	return tenantID, nil
}

func ValidateTenantScope(ctx context.Context, requestedTenantID string) error {
	ctxTenantID, err := TenantIDFromContext(ctx)
	if err != nil {
		return err
	}
	if ctxTenantID != requestedTenantID {
		return fmt.Errorf("%w: context tenant %q does not match requested tenant %q", ErrTenantMismatch, ctxTenantID, requestedTenantID)
	}
	return nil
}
