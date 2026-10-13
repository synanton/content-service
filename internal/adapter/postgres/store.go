package postgres

import (
	"context"
	"crypto/subtle"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/synanton/content-service/internal/config"
	"github.com/synanton/content-service/internal/domain"
	"github.com/synanton/content-service/internal/port"
	"github.com/synanton/content-service/pkg/digest"
	"github.com/synanton/content-service/pkg/reference"
)

type Store struct {
	pool  *pgxpool.Pool
	cfg   *config.Config
	blobs port.BlobStore
}

// NewStore wires the metadata database with the object tier. A nil blobs
// keeps the legacy behavior (payload bytes in Postgres for every tier);
// pass a real BlobStore to persist object-tier bytes in S3/MinIO.
func NewStore(pool *pgxpool.Pool, cfg *config.Config, blobs port.BlobStore) *Store {
	return &Store{pool: pool, cfg: cfg, blobs: blobs}
}

// BlobKey derives the content-addressed object key. The digest already
// binds tenant scope at the reference layer; embedding the tenant keeps
// keys debuggable and lets bucket policies stay tenant-readable.
// Exported for operators and tests reconciling Postgres rows with objects.
func BlobKey(tenantID, digest string) string {
	hex, _, _ := strings.Cut(digest, ":")
	if hex == "" {
		hex = digest
	}
	return tenantID + "/" + hex
}

func blobKey(tenantID, digest string) string {
	return BlobKey(tenantID, digest)
}

func (s *Store) selectTier(size int64) string {
	if size < int64(s.cfg.Storage.InlineThresholdBytes) {
		return "inline"
	}
	if size < int64(s.cfg.Storage.ObjectThresholdBytes) {
		return "parquet"
	}
	return "s3"
}

func (s *Store) Put(ctx context.Context, p port.PutParams) (*port.PutResult, error) {
	if p.TenantID == "" {
		return nil, fmt.Errorf("%w: tenant_id is required", domain.ErrInvalidArgument)
	}
	if len(p.Payload) == 0 {
		return nil, fmt.Errorf("%w: payload is empty", domain.ErrInvalidArgument)
	}
	computed := digest.Compute(p.Payload)
	tier := s.selectTier(int64(len(p.Payload)))
	retentionDays := p.RetentionDays
	if retentionDays <= 0 {
		retentionDays = s.cfg.Retention.DefaultPolicyDays
	}
	expiresAt := time.Now().UTC().Add(time.Duration(retentionDays) * 24 * time.Hour)

	var artifactID, storedDigest string
	err := s.pool.QueryRow(ctx, `
		SELECT artifact_id::text, digest FROM artifacts
		WHERE tenant_id = $1 AND digest = $2`,
		p.TenantID, computed,
	).Scan(&artifactID, &storedDigest)
	if err == nil {
		ref := reference.New(p.TenantID, artifactID, computed, 1, p.SerializerVersion)
		if len(ref.Encode()) > 512 {
			return nil, domain.ErrReferenceTooLong
		}
		if err := s.registerReference(ctx, p.TenantID, computed, p.OwnerID); err != nil {
			return nil, err
		}
		art, err := s.loadArtifact(ctx, p.TenantID, artifactID)
		if err != nil {
			return nil, err
		}
		return &port.PutResult{Artifact: *art, DedupHit: true}, nil
	}
	if err != nil && !isNoRows(err) {
		return nil, fmt.Errorf("dedup lookup: %w", err)
	}

	artifactID = uuid.NewString()
	var inline []byte
	if tier == "inline" {
		inline = p.Payload
	} else {
		if s.blobs == nil {
			return nil, fmt.Errorf("tier %s requires object storage: %w",
				tier, domain.ErrInvalidArgument)
		}
		if err := s.blobs.Put(ctx, blobKey(p.TenantID, computed), p.Payload, p.ContentType); err != nil {
			return nil, err
		}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `
		INSERT INTO artifacts
			(tenant_id, artifact_id, digest, generation, serializer_version,
			 content_type, size_bytes, storage_tier, payload, expires_at)
		VALUES ($1, $2::uuid, $3, 1, $4, $5, $6, $7, $8, $9)
		ON CONFLICT DO NOTHING`,
		p.TenantID, artifactID, computed, p.SerializerVersion,
		p.ContentType, len(p.Payload), tier, inline, expiresAt,
	); err != nil {
		return nil, fmt.Errorf("insert artifact: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO retention_policies
			(tenant_id, artifact_id, owner_id, retention_days, expires_at, legal_hold)
		VALUES ($1, $2::uuid, $3, $4, $5, false)
		ON CONFLICT (tenant_id, artifact_id, owner_id) DO UPDATE
			SET retention_days = EXCLUDED.retention_days,
			    expires_at = EXCLUDED.expires_at,
			    updated_at = now()`,
		p.TenantID, artifactID, ownerOrDefault(p.OwnerID), retentionDays, expiresAt,
	); err != nil {
		return nil, fmt.Errorf("insert retention: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO reference_registry (tenant_id, digest, owner_id, owner_type)
		VALUES ($1, $2, $3, 'commitix_intent')
		ON CONFLICT (tenant_id, digest, owner_id) DO UPDATE
			SET released_at = NULL`,
		p.TenantID, computed, ownerOrDefault(p.OwnerID),
	); err != nil {
		return nil, fmt.Errorf("insert reference: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	art, err := s.loadArtifact(ctx, p.TenantID, artifactID)
	if err != nil {
		return nil, err
	}
	return &port.PutResult{Artifact: *art, DedupHit: false}, nil
}

func (s *Store) Get(ctx context.Context, tenantID, artifactID string) ([]byte, *domain.Artifact, error) {
	if tenantID == "" || artifactID == "" {
		return nil, nil, fmt.Errorf("%w: tenant_id and artifact_id are required", domain.ErrInvalidArgument)
	}
	art, payload, err := s.loadArtifactWithPayload(ctx, tenantID, artifactID)
	if err != nil {
		return nil, nil, err
	}
	if len(payload) == 0 {
		if s.blobs == nil {
			return nil, nil, domain.ErrIntegrityMismatch
		}
		payload, err = s.blobs.Get(ctx, blobKey(tenantID, art.Digest))
		if err != nil {
			return nil, nil, fmt.Errorf("fetch object tier: %w", err)
		}
	}
	if subtle.ConstantTimeCompare([]byte(digest.Compute(payload)), []byte(art.Digest)) != 1 {
		return nil, nil, domain.ErrIntegrityMismatch
	}
	return payload, art, nil
}

func (s *Store) Exists(ctx context.Context, tenantID, artifactID string) (bool, error) {
	if tenantID == "" || artifactID == "" {
		return false, fmt.Errorf("%w: tenant_id and artifact_id are required", domain.ErrInvalidArgument)
	}
	var one int
	err := s.pool.QueryRow(ctx, `
		SELECT 1 FROM artifacts WHERE tenant_id = $1 AND artifact_id = $2::uuid`,
		tenantID, artifactID,
	).Scan(&one)
	if err != nil {
		if isNoRows(err) {
			return false, nil
		}
		return false, fmt.Errorf("exists: %w", err)
	}
	return true, nil
}

func (s *Store) MarkForDeletion(ctx context.Context, tenantID, artifactID, ownerID string) error {
	if tenantID == "" || artifactID == "" {
		return fmt.Errorf("%w: tenant_id and artifact_id are required", domain.ErrInvalidArgument)
	}
	var dgst string
	err := s.pool.QueryRow(ctx, `
		SELECT digest FROM artifacts WHERE tenant_id = $1 AND artifact_id = $2::uuid`,
		tenantID, artifactID,
	).Scan(&dgst)
	if err != nil {
		if isNoRows(err) {
			return domain.ErrNotFound
		}
		return fmt.Errorf("mark lookup: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
		UPDATE reference_registry SET released_at = now()
		WHERE tenant_id = $1 AND digest = $2 AND owner_id = $3 AND released_at IS NULL`,
		tenantID, dgst, ownerOrDefault(ownerID),
	)
	return err
}

func (s *Store) RegisterReference(ctx context.Context, tenantID, dgst, ownerID string) error {
	return s.registerReference(ctx, tenantID, dgst, ownerID)
}

func (s *Store) ReleaseReference(ctx context.Context, tenantID, dgst, ownerID string) error {
	if tenantID == "" || dgst == "" {
		return fmt.Errorf("%w: tenant_id and digest are required", domain.ErrInvalidArgument)
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE reference_registry SET released_at = now()
		WHERE tenant_id = $1 AND digest = $2 AND owner_id = $3 AND released_at IS NULL`,
		tenantID, dgst, ownerOrDefault(ownerID),
	)
	return err
}

func (s *Store) SetLegalHold(ctx context.Context, tenantID, artifactID string, hold bool, actor, reason string) error {
	if tenantID == "" || artifactID == "" {
		return fmt.Errorf("%w: tenant_id and artifact_id are required", domain.ErrInvalidArgument)
	}
	if reason == "" {
		return fmt.Errorf("%w: reason is required for legal hold changes", domain.ErrInvalidArgument)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	cmd, err := tx.Exec(ctx, `
		UPDATE retention_policies SET legal_hold = $3, updated_at = now()
		WHERE tenant_id = $1 AND artifact_id = $2::uuid`,
		tenantID, artifactID, hold,
	)
	if err != nil {
		return fmt.Errorf("set legal hold: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	action := "legal_hold_place"
	if !hold {
		action = "legal_hold_release"
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_log (actor, action, tenant_id, artifact_id, scope, reason)
		VALUES ($1, $2, $3, $4::uuid, 'artifact', $5)`,
		actor, action, tenantID, artifactID, reason,
	); err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	return tx.Commit(ctx)
}

func (s *Store) SetRetention(ctx context.Context, tenantID, artifactID string, retentionDays int) error {
	if tenantID == "" || artifactID == "" {
		return fmt.Errorf("%w: tenant_id and artifact_id are required", domain.ErrInvalidArgument)
	}
	if retentionDays <= 0 {
		return fmt.Errorf("%w: retention_days must be positive", domain.ErrInvalidArgument)
	}
	expiresAt := time.Now().UTC().Add(time.Duration(retentionDays) * 24 * time.Hour)
	cmd, err := s.pool.Exec(ctx, `
		UPDATE retention_policies
		SET retention_days = $3, expires_at = $4, updated_at = now()
		WHERE tenant_id = $1 AND artifact_id = $2::uuid`,
		tenantID, artifactID, retentionDays, expiresAt,
	)
	if err != nil {
		return fmt.Errorf("set retention: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (s *Store) registerReference(ctx context.Context, tenantID, dgst, ownerID string) error {
	if tenantID == "" || dgst == "" {
		return fmt.Errorf("%w: tenant_id and digest are required", domain.ErrInvalidArgument)
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO reference_registry (tenant_id, digest, owner_id, owner_type)
		VALUES ($1, $2, $3, 'commitix_intent')
		ON CONFLICT (tenant_id, digest, owner_id) DO UPDATE SET released_at = NULL`,
		tenantID, dgst, ownerOrDefault(ownerID),
	)
	return err
}

func (s *Store) loadArtifact(ctx context.Context, tenantID, artifactID string) (*domain.Artifact, error) {
	art, _, err := s.loadArtifactWithPayload(ctx, tenantID, artifactID)
	return art, err
}

func (s *Store) loadArtifactWithPayload(ctx context.Context, tenantID, artifactID string) (*domain.Artifact, []byte, error) {
	var art domain.Artifact
	var payload []byte
	var expires interface{}
	err := s.pool.QueryRow(ctx, `
		SELECT tenant_id, artifact_id::text, digest, generation, serializer_version,
		       COALESCE(content_type, ''), size_bytes, storage_tier, payload, created_at, expires_at
		FROM artifacts WHERE tenant_id = $1 AND artifact_id = $2::uuid`,
		tenantID, artifactID,
	).Scan(
		&art.TenantID, &art.ArtifactID, &art.Digest, &art.Generation,
		&art.SerializerVersion, &art.ContentType, &art.SizeBytes,
		&art.StorageTier, &payload, &art.CreatedAt, &expires,
	)
	if err != nil {
		if isNoRows(err) {
			return nil, nil, domain.ErrNotFound
		}
		return nil, nil, fmt.Errorf("load artifact: %w", err)
	}
	return &art, payload, nil
}

func ownerOrDefault(owner string) string {
	if owner == "" {
		return "unknown"
	}
	return owner
}

func isNoRows(err error) bool {
	return err == pgx.ErrNoRows
}
