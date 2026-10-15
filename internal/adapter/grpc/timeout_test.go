package grpc

import (
	"context"
	"fmt"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestToStatusTimeout pins the timeout mapping: backend deadlines are
// Unavailable (retryable), never Internal or a fabricated payload.
func TestToStatusTimeout(t *testing.T) {
	err := toStatus(fmt.Errorf("fetch object tier: %w", context.DeadlineExceeded))
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("code = %v, want Unavailable", status.Code(err))
	}
}
