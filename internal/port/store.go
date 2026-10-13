package port

import (
	"context"

	"github.com/synanton/content-service/internal/domain"
)

type PutParams struct {
	TenantID          string
	Payload           []byte
	ContentType       string
	OwnerID           string
	RetentionDays     int
	SerializerVersion int
}

type PutResult struct {
	Artifact domain.Artifact
	DedupHit bool
}

type ContentStore interface {
	Put(ctx context.Context, p PutParams) (*PutResult, error)
	Get(ctx context.Context, tenantID, artifactID string) ([]byte, *domain.Artifact, error)
	Exists(ctx context.Context, tenantID, artifactID string) (bool, error)
	MarkForDeletion(ctx context.Context, tenantID, artifactID, ownerID string) error
	RegisterReference(ctx context.Context, tenantID, digest, ownerID string) error
	ReleaseReference(ctx context.Context, tenantID, digest, ownerID string) error
	SetLegalHold(ctx context.Context, tenantID, artifactID string, hold bool, actor, reason string) error
	SetRetention(ctx context.Context, tenantID, artifactID string, retentionDays int) error
}
