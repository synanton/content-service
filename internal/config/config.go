package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	DB            DBConfig
	S3            S3Config
	Storage       StorageConfig
	Retention     RetentionConfig
	Integrity     IntegrityConfig
	Orphan        OrphanConfig
	Security      SecurityConfig
	Observability ObservabilityConfig
	Server        ServerConfig
	Jobs          JobsConfig
}

type JobsConfig struct {
	DispositionInterval time.Duration
	IntegrityInterval   time.Duration
	OrphanInterval      time.Duration
	CompactorInterval   time.Duration
	BatchSize           int
	ShardIndex          int
	ShardCount          int
}

type DBConfig struct {
	URL            string
	MaxConnections int
}

type S3Config struct {
	Endpoint     string
	Bucket       string
	Region       string
	AccessKey    string
	SecretKey    string
	UsePathStyle bool
	// Per-operation deadlines (C-4). OpTimeout bounds point operations
	// (Put/Get/Stat/Delete/Range); ListTimeout bounds reconciliation
	// scans, which legitimately run longer. Both cap total time
	// including retries: a slow-but-not-failed backend fails closed
	// instead of stalling callers.
	OpTimeout   time.Duration
	ListTimeout time.Duration
	// Retry budget for idempotent operations. Attempts counts total
	// tries (1 initial + retries), backoff doubles from base per try.
	// Only retryable failures (5xx, network errors) retry; 4xx,
	// missing objects, and validation errors never do.
	MaxAttempts    int
	RetryBaseDelay time.Duration
	// Circuit breaker: Threshold consecutive failures open the breaker
	// for Cooldown, failing fast without touching the network. A
	// successful probe half-closes it. Protects control loops (jobs,
	// readiness) from retry storms against a dead backend.
	BreakerThreshold int
	BreakerCooldown  time.Duration
}

type StorageConfig struct {
	InlineThresholdBytes int
	ObjectThresholdBytes int
	ParquetBatchSize     int
	ParquetCompression   string
	ParquetTargetRows    int
	ParquetMaxFiles      int
}

type RetentionConfig struct {
	DefaultPolicyDays      int
	LegalHoldEnabled       bool
	DispositionGracePeriod time.Duration
}

type IntegrityConfig struct {
	VerifyOnRead         bool
	VerifyOnPublish      bool
	VerificationInterval time.Duration
	SampleRate           float64
}

type OrphanConfig struct {
	GracePeriod   time.Duration
	SweepInterval time.Duration
	DryRun        bool
}

type SecurityConfig struct {
	MTLSCert     string
	MTLSKey      string
	KMSKeyID     string
	AuditEnabled bool
	Auth         AuthConfig
}

// AuthConfig controls validation of Security Provider contexts
// (joint-plan D.1/D.2). Disabled by default: when disabled, tenant
// scope behaves exactly as before (X-Tenant-ID header, documented as
// unauthenticated). When enabled, the tenant is derived from the
// validated context and caller hints are cross-checked.
type AuthConfig struct {
	Enabled bool
	HMACKey string
	Issuer  string
}

type ObservabilityConfig struct {
	LogLevel        string
	MetricsEnabled  bool
	TracingEnabled  bool
	TracingEndpoint string
}

type ServerConfig struct {
	Port             int
	Host             string
	GrpcPort         int
	APIKey           string
	MigrateOnStartup bool
	MigrationsDir    string
}

func Load() (*Config, error) {
	cfg := &Config{
		DB: DBConfig{
			URL:            getEnv("CONTENT_DB_URL", "postgres://localhost:5432/content?sslmode=disable"),
			MaxConnections: getEnvInt("CONTENT_DB_MAX_CONNECTIONS", 20),
		},
		S3: S3Config{
			Endpoint:         getEnv("CONTENT_S3_ENDPOINT", "http://localhost:9000"),
			Bucket:           getEnv("CONTENT_S3_BUCKET", "synanton-content"),
			Region:           getEnv("CONTENT_S3_REGION", "us-east-1"),
			AccessKey:        getEnv("CONTENT_S3_ACCESS_KEY", "minioadmin"),
			SecretKey:        getEnv("CONTENT_S3_SECRET_KEY", "minioadmin"),
			UsePathStyle:     getEnvBool("CONTENT_S3_USE_PATH_STYLE", true),
			OpTimeout:        getEnvDuration("CONTENT_S3_OP_TIMEOUT", 30*time.Second),
			ListTimeout:      getEnvDuration("CONTENT_S3_LIST_TIMEOUT", 2*time.Minute),
			MaxAttempts:      getEnvInt("CONTENT_S3_MAX_ATTEMPTS", 3),
			RetryBaseDelay:   getEnvDuration("CONTENT_S3_RETRY_BASE_DELAY", 200*time.Millisecond),
			BreakerThreshold: getEnvInt("CONTENT_S3_BREAKER_THRESHOLD", 5),
			BreakerCooldown:  getEnvDuration("CONTENT_S3_BREAKER_COOLDOWN", 30*time.Second),
		},
		Storage: StorageConfig{
			InlineThresholdBytes: getEnvInt("CONTENT_INLINE_THRESHOLD_BYTES", 4096),
			ObjectThresholdBytes: getEnvInt("CONTENT_OBJECT_THRESHOLD_BYTES", 262144),
			ParquetBatchSize:     getEnvInt("CONTENT_PARQUET_BATCH_SIZE", 1000),
			ParquetCompression:   getEnv("CONTENT_PARQUET_COMPRESSION", "zstd"),
			ParquetTargetRows:    getEnvInt("CONTENT_PARQUET_TARGET_ROWS", 1000),
			ParquetMaxFiles:      getEnvInt("CONTENT_PARQUET_MAX_FILES", 32),
		},
		Retention: RetentionConfig{
			DefaultPolicyDays:      getEnvInt("CONTENT_RETENTION_DEFAULT_DAYS", 90),
			LegalHoldEnabled:       getEnvBool("CONTENT_LEGAL_HOLD_ENABLED", true),
			DispositionGracePeriod: getEnvDuration("CONTENT_DISPOSITION_GRACE_PERIOD", 72*time.Hour),
		},
		Integrity: IntegrityConfig{
			VerifyOnRead:         getEnvBool("CONTENT_VERIFY_ON_READ", false),
			VerifyOnPublish:      getEnvBool("CONTENT_VERIFY_ON_PUBLISH", true),
			VerificationInterval: getEnvDuration("CONTENT_VERIFICATION_INTERVAL", 24*time.Hour),
			SampleRate:           getEnvFloat("CONTENT_SAMPLE_RATE", 0.01),
		},
		Orphan: OrphanConfig{
			GracePeriod:   getEnvDuration("CONTENT_ORPHAN_GRACE_PERIOD", 24*time.Hour),
			SweepInterval: getEnvDuration("CONTENT_ORPHAN_SWEEP_INTERVAL", time.Hour),
			DryRun:        getEnvBool("CONTENT_ORPHAN_DRY_RUN", false),
		},
		Security: SecurityConfig{
			MTLSCert:     getEnv("CONTENT_MTLS_CERT", ""),
			MTLSKey:      getEnv("CONTENT_MTLS_KEY", ""),
			KMSKeyID:     getEnv("CONTENT_KMS_KEY_ID", ""),
			AuditEnabled: getEnvBool("CONTENT_AUDIT_ENABLED", true),
			Auth: AuthConfig{
				Enabled: getEnvBool("CONTENT_AUTH_ENABLED", false),
				HMACKey: getEnv("CONTENT_AUTH_HMAC_KEY", ""),
				Issuer:  getEnv("CONTENT_AUTH_ISSUER", "synanton-security-provider"),
			},
		},
		Observability: ObservabilityConfig{
			LogLevel:        getEnv("CONTENT_LOG_LEVEL", "info"),
			MetricsEnabled:  getEnvBool("CONTENT_METRICS_ENABLED", true),
			TracingEnabled:  getEnvBool("CONTENT_TRACING_ENABLED", true),
			TracingEndpoint: getEnv("CONTENT_OTEL_ENDPOINT", ""),
		},
		Server: ServerConfig{
			Host:             getEnv("CONTENT_SERVER_HOST", "0.0.0.0"),
			Port:             getEnvInt("CONTENT_SERVER_PORT", 8080),
			GrpcPort:         getEnvInt("CONTENT_GRPC_PORT", 50051),
			APIKey:           getEnv("CONTENT_API_KEY", ""),
			MigrateOnStartup: getEnvBool("CONTENT_MIGRATE_ON_STARTUP", false),
			MigrationsDir:    getEnv("CONTENT_MIGRATIONS_DIR", "migrations"),
		},
		Jobs: JobsConfig{
			DispositionInterval: getEnvDuration("CONTENT_JOBS_DISPOSITION_INTERVAL", time.Hour),
			IntegrityInterval:   getEnvDuration("CONTENT_JOBS_INTEGRITY_INTERVAL", 24*time.Hour),
			OrphanInterval:      getEnvDuration("CONTENT_JOBS_ORPHAN_INTERVAL", time.Hour),
			CompactorInterval:   getEnvDuration("CONTENT_JOBS_COMPACTOR_INTERVAL", 6*time.Hour),
			BatchSize:           getEnvInt("CONTENT_JOBS_BATCH_SIZE", 1000),
			ShardIndex:          getEnvInt("CONTENT_JOBS_SHARD_INDEX", 0),
			ShardCount:          getEnvInt("CONTENT_JOBS_SHARD_COUNT", 1),
		},
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

func (c *Config) Validate() error {
	if c.Storage.InlineThresholdBytes < 0 {
		return fmt.Errorf("inline threshold must be non-negative")
	}
	if c.Storage.ObjectThresholdBytes <= c.Storage.InlineThresholdBytes {
		return fmt.Errorf("object threshold must be greater than inline threshold")
	}
	if c.Storage.ParquetTargetRows <= 1 {
		return fmt.Errorf("parquet target rows must exceed 1")
	}
	if c.Storage.ParquetMaxFiles < 2 {
		return fmt.Errorf("parquet max files must be at least 2 (merging one file is a no-op)")
	}
	if c.Retention.DefaultPolicyDays < 0 {
		return fmt.Errorf("default retention policy days must be non-negative")
	}
	if c.Integrity.SampleRate < 0 || c.Integrity.SampleRate > 1 {
		return fmt.Errorf("sample rate must be between 0 and 1")
	}
	if c.Jobs.BatchSize <= 0 {
		return fmt.Errorf("jobs batch size must be positive")
	}
	if c.Jobs.ShardCount <= 0 || c.Jobs.ShardIndex < 0 || c.Jobs.ShardIndex >= c.Jobs.ShardCount {
		return fmt.Errorf("jobs shard index must satisfy 0 <= index < count")
	}
	if c.Security.Auth.Enabled && len(c.Security.Auth.HMACKey) < 32 {
		return fmt.Errorf("auth HMAC key must be at least 32 bytes when CONTENT_AUTH_ENABLED=true")
	}
	if c.S3.OpTimeout <= 0 {
		return fmt.Errorf("s3 op timeout must be positive")
	}
	if c.S3.ListTimeout <= 0 {
		return fmt.Errorf("s3 list timeout must be positive")
	}
	if c.S3.MaxAttempts < 1 {
		return fmt.Errorf("s3 max attempts must be at least 1 (no retry)")
	}
	if c.S3.RetryBaseDelay < 0 {
		return fmt.Errorf("s3 retry base delay must be non-negative")
	}
	if c.S3.BreakerThreshold < 1 {
		return fmt.Errorf("s3 breaker threshold must be at least 1")
	}
	if c.S3.BreakerCooldown < 0 {
		return fmt.Errorf("s3 breaker cooldown must be non-negative")
	}
	return nil
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if intValue, err := strconv.Atoi(value); err == nil {
			return intValue
		}
	}
	return defaultValue
}

func getEnvBool(key string, defaultValue bool) bool {
	if value := os.Getenv(key); value != "" {
		if boolValue, err := strconv.ParseBool(value); err == nil {
			return boolValue
		}
	}
	return defaultValue
}

func getEnvFloat(key string, defaultValue float64) float64 {
	if value := os.Getenv(key); value != "" {
		if floatValue, err := strconv.ParseFloat(value, 64); err == nil {
			return floatValue
		}
	}
	return defaultValue
}

func getEnvDuration(key string, defaultValue time.Duration) time.Duration {
	if value := os.Getenv(key); value != "" {
		if duration, err := time.ParseDuration(value); err == nil {
			return duration
		}
	}
	return defaultValue
}
