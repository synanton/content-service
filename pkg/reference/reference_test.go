package reference

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestEncodeParseRoundTrip(t *testing.T) {
	ref := New("acme", uuid.NewString(), "sha256:abc123", 1, 3)
	parsed, err := Parse(ref.Encode())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if *parsed != *ref {
		t.Fatalf("round trip mismatch: %+v vs %+v", parsed, ref)
	}
}

func TestParseRejectsMissingVersionPrefix(t *testing.T) {
	if _, err := Parse("tenant_id=acme"); err == nil {
		t.Fatal("missing version prefix must fail")
	}
}

func TestParseRejectsMissingFields(t *testing.T) {
	if _, err := Parse("v1:artifact_id=x&digest=sha256:y"); err == nil {
		t.Fatal("missing tenant_id must fail")
	}
}

func TestEncodeEscapesSpecialCharacters(t *testing.T) {
	ref := New("acme&co", uuid.NewString(), "sha256:abc123", 1, 1)
	parsed, err := Parse(ref.Encode())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.TenantID != "acme&co" {
		t.Fatalf("tenant_id = %q, want acme&co", parsed.TenantID)
	}
}

func TestEncodeStaysWithin512Chars(t *testing.T) {
	ref := New("acme", uuid.NewString(), "sha256:abc123", 1, 1)
	if len(ref.Encode()) > 512 {
		t.Fatal("encoded reference must stay within 512 characters")
	}
}

func TestParseDefaultsGeneration(t *testing.T) {
	parsed, err := Parse("v1:tenant_id=acme&artifact_id=" + uuid.NewString() + "&digest=sha256:abc123")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.Generation != 1 || parsed.SerializerVersion != 1 {
		t.Fatalf("missing fields must default to 1: %+v", parsed)
	}
}

func TestNewRejectsBlankTenant(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("blank tenant must panic")
		}
	}()
	_ = New("", uuid.NewString(), "sha256:abc123", 1, 1)
}

func BenchmarkEncode(b *testing.B) {
	ref := New("acme", "550e8400-e29b-41d4-a716-446655440000", "sha256:abc123", 1, 3)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ref.Encode()
	}
}

func BenchmarkParse(b *testing.B) {
	raw := New("acme", "550e8400-e29b-41d4-a716-446655440000", "sha256:abc123", 1, 3).Encode()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Parse(raw); err != nil {
			b.Fatalf("parse: %v", err)
		}
	}
}

func TestEncodeHasVersionPrefix(t *testing.T) {
	ref := New("acme", uuid.NewString(), "sha256:abc123", 1, 1)
	if !strings.HasPrefix(ref.Encode(), "v1:") {
		t.Fatalf("encoded reference %q must carry the v1: prefix", ref.Encode())
	}
}
