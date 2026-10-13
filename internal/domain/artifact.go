package domain

import (
	"errors"
	"time"
)

var (
	ErrNotFound          = errors.New("content: not found")
	ErrTenantMismatch    = errors.New("content: tenant mismatch")
	ErrIntegrityMismatch = errors.New("content: integrity mismatch")
	ErrLegalHold         = errors.New("content: legal hold blocks disposition")
	ErrInvalidArgument   = errors.New("content: invalid argument")
	ErrReferenceTooLong  = errors.New("content: reference exceeds 512 characters")
)

type Artifact struct {
	TenantID          string
	ArtifactID        string
	Digest            string
	Generation        int
	SerializerVersion int
	ContentType       string
	SizeBytes         int64
	StorageTier       string
	CreatedAt         time.Time
	ExpiresAt         *time.Time
	// ParquetFileKey and ParquetRow locate the payload inside a merged
	// Parquet file. Empty key means the legacy per-artifact object
	// (BlobKey) or inline bytes, depending on the tier.
	ParquetFileKey string
	ParquetRow     int64
}

type RetentionPolicy struct {
	TenantID      string
	ArtifactID    string
	OwnerID       string
	RetentionDays int
	ExpiresAt     time.Time
	LegalHold     bool
}

type AuditEvent struct {
	Actor       string
	Action      string
	TenantID    string
	ArtifactID  string
	Scope       string
	Reason      string
	BeforeState string
	AfterState  string
}
