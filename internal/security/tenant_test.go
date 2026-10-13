package security

import (
	"context"
	"testing"
)

func TestWithTenantID_ReturnsContextWithTenantID(t *testing.T) {
	ctx := WithTenantID(context.Background(), "acme")
	tenantID, err := TenantIDFromContext(ctx)
	if err != nil {
		t.Fatalf("Expected tenant ID from context, got error: %v", err)
	}
	if tenantID != "acme" {
		t.Fatalf("Expected tenant ID 'acme', got %q", tenantID)
	}
}

func TestTenantIDFromContext_ReturnsErrorWhenMissing(t *testing.T) {
	_, err := TenantIDFromContext(context.Background())
	if err == nil {
		t.Fatal("Expected error when tenant ID is missing from context")
	}
}

func TestValidateTenantScope_MatchingTenants(t *testing.T) {
	ctx := WithTenantID(context.Background(), "acme")
	err := ValidateTenantScope(ctx, "acme")
	if err != nil {
		t.Fatalf("Expected no error for matching tenants, got: %v", err)
	}
}

func TestValidateTenantScope_MismatchedTenants(t *testing.T) {
	ctx := WithTenantID(context.Background(), "acme")
	err := ValidateTenantScope(ctx, "other-tenant")
	if err == nil {
		t.Fatal("Expected error for mismatched tenants")
	}
}


