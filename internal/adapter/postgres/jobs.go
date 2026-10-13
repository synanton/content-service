package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/synanton/content-service/internal/jobs"
)

// Compile-time check that the adapter satisfies the jobs port.
var _ jobs.LifecycleStore = (*Store)(nil)

// ListDispositionCandidates returns artifacts fully expired beyond the cutoff
// with no legal hold on any owner row and no unreleased references.
func (s *Store) ListDispositionCandidates(
	ctx context.Context, cutoff time.Time, limit int,
) ([]jobs.ExpiredCandidate, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT a.tenant_id, a.artifact_id::text, a.digest, a.storage_tier
		FROM artifacts a
		WHERE NOT EXISTS (
			SELECT 1 FROM retention_policies r
			WHERE r.tenant_id = a.tenant_id
			  AND r.artifact_id = a.artifact_id
			  AND (r.legal_hold OR r.expires_at > $1)
		)
		AND NOT EXISTS (
			SELECT 1 FROM reference_registry g
			WHERE g.tenant_id = a.tenant_id
			  AND g.digest = a.digest
			  AND g.released_at IS NULL
		)
		ORDER BY a.created_at
		LIMIT $2`,
		cutoff, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list disposition candidates: %w", err)
	}
	defer rows.Close()
	var out []jobs.ExpiredCandidate
	for rows.Next() {
		var c jobs.ExpiredCandidate
		if err := rows.Scan(&c.TenantID, &c.ArtifactID, &c.Digest, &c.StorageTier); err != nil {
			return nil, fmt.Errorf("scan disposition candidate: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DisposeArtifact deletes one artifact row (retention and reference rows
// follow via ON DELETE CASCADE) after re-checking legal hold and live
// references inside the same statement. Parquet file rows left with zero
// referencing artifacts are reaped in the same transaction so the sweeper
// can collect their objects. deleted=false means a concurrent hold or
// reference won the race; the sweeper retries next tick.
func (s *Store) DisposeArtifact(
	ctx context.Context, tenantID, artifactID, actor string,
) (jobs.DisposedArtifact, bool, error) {
	var out jobs.DisposedArtifact
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return out, false, err
	}
	defer tx.Rollback(ctx)
	var fileID *string
	err = tx.QueryRow(ctx, `
		DELETE FROM artifacts a USING (
			SELECT tenant_id, artifact_id, digest, storage_tier, parquet_file_id FROM artifacts
			WHERE tenant_id = $1 AND artifact_id = $2::uuid
		) AS t
		WHERE a.tenant_id = t.tenant_id AND a.artifact_id = t.artifact_id
		  AND NOT EXISTS (
			SELECT 1 FROM retention_policies r
			WHERE r.tenant_id = t.tenant_id AND r.artifact_id = t.artifact_id AND r.legal_hold
		  )
		  AND NOT EXISTS (
			SELECT 1 FROM reference_registry g
			WHERE g.tenant_id = t.tenant_id AND g.digest = t.digest AND g.released_at IS NULL
		  )
		RETURNING t.digest, t.storage_tier, t.parquet_file_id`,
		tenantID, artifactID,
	).Scan(&out.Digest, &out.Tier, &fileID)
	if err != nil {
		if isNoRows(err) {
			return out, false, nil
		}
		return out, false, fmt.Errorf("dispose artifact: %w", err)
	}
	if fileID != nil {
		if err := tx.QueryRow(ctx, `
			WITH reaped AS (
				DELETE FROM parquet_files pf
				WHERE pf.file_id = $1::uuid
				  AND NOT EXISTS (
					SELECT 1 FROM artifacts a WHERE a.parquet_file_id = pf.file_id
				  )
				RETURNING s3_key
			) SELECT s3_key FROM reaped`,
			*fileID,
		).Scan(&out.FileKey); err != nil && !isNoRows(err) {
			return out, false, fmt.Errorf("dispose reap file: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_log (actor, action, tenant_id, artifact_id, scope, reason)
		VALUES ($1, 'disposition_delete', $2, $3::uuid, 'artifact', 'retention expired')`,
		actor, tenantID, artifactID,
	); err != nil {
		return out, false, fmt.Errorf("dispose audit: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return out, false, fmt.Errorf("dispose commit: %w", err)
	}
	return out, true, nil
}

// ListTenants returns distinct tenants with artifacts.
func (s *Store) ListTenants(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT tenant_id FROM artifacts ORDER BY tenant_id`)
	if err != nil {
		return nil, fmt.Errorf("list tenants: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var tenant string
		if err := rows.Scan(&tenant); err != nil {
			return nil, fmt.Errorf("scan tenant: %w", err)
		}
		out = append(out, tenant)
	}
	return out, rows.Err()
}

// ListArtifactIDs returns up to limit artifact ids for a tenant, sampling
// the given fraction (1.0 verifies everything).
func (s *Store) ListArtifactIDs(
	ctx context.Context, tenantID string, limit int, sampleRate float64,
) ([]string, error) {
	if sampleRate <= 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT artifact_id::text FROM artifacts
		WHERE tenant_id = $1 AND random() < $2
		ORDER BY created_at
		LIMIT $3`,
		tenantID, sampleRate, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list artifact ids: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan artifact id: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// RecordIntegrityFailure audits a corrupt or missing-payload artifact.
func (s *Store) RecordIntegrityFailure(
	ctx context.Context, tenantID, artifactID, detail string,
) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO audit_log (actor, action, tenant_id, artifact_id, scope, reason)
		VALUES ('system/integrity', 'integrity_mismatch', $1, $2::uuid, 'artifact', $3)`,
		tenantID, artifactID, detail,
	)
	if err != nil {
		return fmt.Errorf("record integrity failure: %w", err)
	}
	return nil
}

// ListExternalArtifacts returns artifacts whose bytes live in object storage.
func (s *Store) ListExternalArtifacts(ctx context.Context, limit int) ([]jobs.ExternalArtifact, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT tenant_id, artifact_id::text, digest, storage_tier FROM artifacts
		WHERE storage_tier <> 'inline'
		ORDER BY created_at
		LIMIT $1`,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list external artifacts: %w", err)
	}
	defer rows.Close()
	var out []jobs.ExternalArtifact
	for rows.Next() {
		var a jobs.ExternalArtifact
		if err := rows.Scan(&a.TenantID, &a.ArtifactID, &a.Digest, &a.StorageTier); err != nil {
			return nil, fmt.Errorf("scan external artifact: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ArtifactExistsByDigest reports whether any artifact row holds the digest.
func (s *Store) ArtifactExistsByDigest(ctx context.Context, tenantID, digest string) (bool, error) {
	var one int
	err := s.pool.QueryRow(ctx, `
		SELECT 1 FROM artifacts WHERE tenant_id = $1 AND digest = $2`,
		tenantID, digest,
	).Scan(&one)
	if err != nil {
		if isNoRows(err) {
			return false, nil
		}
		return false, fmt.Errorf("artifact by digest: %w", err)
	}
	return true, nil
}

// ListSmallParquetFiles returns files below the target row count, oldest
// first, for merge candidacy.
func (s *Store) ListSmallParquetFiles(
	ctx context.Context, tenantID string, maxRows, limit int,
) ([]jobs.ParquetFile, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT file_id::text, tenant_id, s3_key, record_count, size_bytes FROM parquet_files
		WHERE tenant_id = $1 AND record_count < $2
		ORDER BY created_at
		LIMIT $3`,
		tenantID, maxRows, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list small parquet files: %w", err)
	}
	defer rows.Close()
	var out []jobs.ParquetFile
	for rows.Next() {
		var f jobs.ParquetFile
		if err := rows.Scan(&f.FileID, &f.Tenant, &f.Key, &f.Rows, &f.Bytes); err != nil {
			return nil, fmt.Errorf("scan parquet file: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// InsertParquetFile registers a newly written merged file.
func (s *Store) InsertParquetFile(ctx context.Context, file jobs.ParquetFile) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO parquet_files (file_id, tenant_id, s3_key, record_count, size_bytes)
		VALUES ($1::uuid, $2, $3, $4, $5)`,
		file.FileID, file.Tenant, file.Key, file.Rows, file.Bytes,
	)
	if err != nil {
		return fmt.Errorf("insert parquet file: %w", err)
	}
	return nil
}

// SwapParquetFilePointers moves every artifact row from one file to another,
// shifting row numbers by the offset the old file's rows start at in the
// merged file. The merged file concatenates sources in order, so the shift
// is exact with no per-row bookkeeping.
func (s *Store) SwapParquetFilePointers(
	ctx context.Context, tenantID, oldFileID, newFileID string, rowOffset int64,
) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE artifacts
		SET parquet_file_id = $3::uuid, parquet_row_idx = parquet_row_idx + $4
		WHERE tenant_id = $1 AND parquet_file_id = $2::uuid`,
		tenantID, oldFileID, newFileID, rowOffset,
	)
	if err != nil {
		return fmt.Errorf("swap parquet pointers: %w", err)
	}
	return nil
}

// DeleteParquetFile removes a file row left with no referencing artifacts.
// It refuses when references remain, so a crashed merge can never strand rows.
func (s *Store) DeleteParquetFile(ctx context.Context, fileID string) error {
	cmd, err := s.pool.Exec(ctx, `
		DELETE FROM parquet_files pf
		WHERE pf.file_id = $1::uuid
		  AND NOT EXISTS (SELECT 1 FROM artifacts a WHERE a.parquet_file_id = pf.file_id)`,
		fileID,
	)
	if err != nil {
		return fmt.Errorf("delete parquet file: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return fmt.Errorf("parquet file %s still referenced or missing", fileID)
	}
	return nil
}

// ParquetFileKeyExists reports whether an object key is a live merged file.
func (s *Store) ParquetFileKeyExists(ctx context.Context, key string) (bool, error) {
	var one int
	err := s.pool.QueryRow(ctx, `SELECT 1 FROM parquet_files WHERE s3_key = $1`, key).Scan(&one)
	if err != nil {
		if isNoRows(err) {
			return false, nil
		}
		return false, fmt.Errorf("parquet file key lookup: %w", err)
	}
	return true, nil
}

// ListParquetCandidates returns parquet-tier artifacts awaiting compaction.
func (s *Store) ListParquetCandidates(ctx context.Context, limit int) ([]jobs.ParquetCandidate, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT tenant_id, artifact_id::text, size_bytes FROM artifacts
		WHERE storage_tier = 'parquet'
		ORDER BY created_at
		LIMIT $1`,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list parquet candidates: %w", err)
	}
	defer rows.Close()
	var out []jobs.ParquetCandidate
	for rows.Next() {
		var c jobs.ParquetCandidate
		if err := rows.Scan(&c.TenantID, &c.ArtifactID, &c.SizeBytes); err != nil {
			return nil, fmt.Errorf("scan parquet candidate: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
