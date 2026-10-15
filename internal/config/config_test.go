package config

import (
	"testing"
	"time"
)

func testBaseConfig() Config {
	return Config{
		Storage: StorageConfig{
			InlineThresholdBytes: 4096,
			ObjectThresholdBytes: 262144,
			ParquetTargetRows:    1000,
			ParquetMaxFiles:      32,
		},
		Retention: RetentionConfig{DefaultPolicyDays: 90},
		Integrity: IntegrityConfig{SampleRate: 0.01},
		Jobs:      JobsConfig{BatchSize: 100, ShardIndex: 0, ShardCount: 1},
		S3: S3Config{
			OpTimeout:        30 * time.Second,
			ListTimeout:      2 * time.Minute,
			MaxAttempts:      3,
			RetryBaseDelay:   200 * time.Millisecond,
			BreakerThreshold: 5,
			BreakerCooldown:  30 * time.Second,
		},
	}
}

// TestS3DeadlineValidation pins the fail-fast contract: incoherent
// deadline/retry/breaker values block startup. A silent fallback would
// mask a policy absence that BB-4 is designed to detect, so every bound
// rejects explicitly.
func TestS3DeadlineValidation(t *testing.T) {
	base := testBaseConfig()
	cases := []struct {
		name   string
		mutate func(*Config)
	}{
		{"zero op timeout", func(c *Config) { c.S3.OpTimeout = 0 }},
		{"negative op timeout", func(c *Config) { c.S3.OpTimeout = -time.Second }},
		{"zero list timeout", func(c *Config) { c.S3.ListTimeout = 0 }},
		{"zero attempts", func(c *Config) { c.S3.MaxAttempts = 0 }},
		{"negative retry delay", func(c *Config) { c.S3.RetryBaseDelay = -time.Second }},
		{"zero breaker threshold", func(c *Config) { c.S3.BreakerThreshold = 0 }},
		{"negative breaker cooldown", func(c *Config) { c.S3.BreakerCooldown = -time.Second }},
	}
	for _, tc := range cases {
		cfg := base
		tc.mutate(&cfg)
		if err := cfg.Validate(); err == nil {
			t.Errorf("%s accepted", tc.name)
		}
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("defaults rejected: %v", err)
	}
}
