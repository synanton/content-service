package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	DB       DBConfig
	S3       S3Config
	Storage  StorageConfig
	Retention RetentionConfig
	Integrity IntegrityConfig
	Orphan   OrphanConfig
	Security SecurityConfig
	Observability ObservabilityConfig
	Server   ServerConfig
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
}

type StorageConfig struct {
	InlineThresholdBytes  int
	ObjectThresholdBytes  int
	ParquetBatchSize      int
	ParquetCompression    string
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
	GracePeriod  time.Duration
	SweepInterval time.Duration
	DryRun       bool
}

type SecurityConfig struct {
	MTLSCert     string
	MTLSKey      string
	KMSKeyID     string
	AuditEnabled bool
}

type ObservabilityConfig struct {
	LogLevel       string
	MetricsEnabled bool
	TracingEnabled bool
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
			Endpoint:     getEnv("CONTENT_S3_ENDPOINT", "http://localhost:9000"),
			Bucket:       getEnv("CONTENT_S3_BUCKET", "synanton-content"),
			Region:       getEnv("CONTENT_S3_REGION", "us-east-1"),
			AccessKey:    getEnv("CONTENT_S3_ACCESS_KEY", "minioadmin"),
			SecretKey:    getEnv("CONTENT_S3_SECRET_KEY", "minioadmin"),
			UsePathStyle: getEnvBool("CONTENT_S3_USE_PATH_STYLE", true),
		},
		Storage: StorageConfig{
			InlineThresholdBytes: getEnvInt("CONTENT_INLINE_THRESHOLD_BYTES", 4096),
			ObjectThresholdBytes: getEnvInt("CONTENT_OBJECT_THRESHOLD_BYTES", 262144),
			ParquetBatchSize:     getEnvInt("CONTENT_PARQUET_BATCH_SIZE", 1000),
			ParquetCompression:   getEnv("CONTENT_PARQUET_COMPRESSION", "zstd"),
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
	if c.Retention.DefaultPolicyDays < 0 {
		return fmt.Errorf("default retention policy days must be non-negative")
	}
	if c.Integrity.SampleRate < 0 || c.Integrity.SampleRate > 1 {
		return fmt.Errorf("sample rate must be between 0 and 1")
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
