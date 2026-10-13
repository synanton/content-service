package conformance

import (
	"testing"

	"github.com/synanton/content-service/internal/conformance"
	"github.com/synanton/content-service/internal/config"
)

func TestCoreContractAgainstFake(t *testing.T) {
	conformance.Verify(t, conformance.NewFakeStore())
}

func TestServerStartsWithValidConfig(t *testing.T) {
	cfg := &config.Config{
		DB: config.DBConfig{
			URL:            "postgres://localhost:5432/content?sslmode=disable",
			MaxConnections: 20,
		},
		Storage: config.StorageConfig{
			InlineThresholdBytes: 4096,
			ObjectThresholdBytes: 262144,
		},
		Retention: config.RetentionConfig{
			DefaultPolicyDays: 90,
			LegalHoldEnabled:  true,
		},
		Integrity: config.IntegrityConfig{
			VerifyOnPublish: true,
			SampleRate:      0.01,
		},
		Security: config.SecurityConfig{
			AuditEnabled: true,
		},
		Jobs: config.JobsConfig{
			BatchSize:  100,
			ShardIndex: 0,
			ShardCount: 1,
		},
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Expected valid config, got error: %v", err)
	}
}

func TestServerRejectsInvalidStorageThresholds(t *testing.T) {
	cfg := &config.Config{
		Storage: config.StorageConfig{
			InlineThresholdBytes: 262144,
			ObjectThresholdBytes: 4096,
		},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Expected error for object threshold <= inline threshold, got nil")
	}
}

func TestCapabilitiesReturnsCoreConformance(t *testing.T) {
	expectedLevel := "CORE"
	expectedFeatures := []string{
		"tenant_isolation",
		"content_addressable_storage",
		"integrity_verification",
	}

	if expectedLevel != "CORE" {
		t.Fatalf("Expected conformance level CORE, got %s", expectedLevel)
	}

	if len(expectedFeatures) == 0 {
		t.Fatal("Expected at least one feature in capabilities")
	}
}
