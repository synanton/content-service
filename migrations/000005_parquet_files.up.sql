CREATE TABLE IF NOT EXISTS parquet_files (
    file_id      UUID        PRIMARY KEY,
    tenant_id    TEXT        NOT NULL,
    s3_key       TEXT        NOT NULL UNIQUE,
    record_count INTEGER     NOT NULL DEFAULT 0,
    size_bytes   BIGINT      NOT NULL DEFAULT 0,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_parquet_files_tenant_created
    ON parquet_files (tenant_id, created_at);

ALTER TABLE artifacts
    ADD COLUMN IF NOT EXISTS parquet_file_id UUID REFERENCES parquet_files (file_id) ON DELETE SET NULL;

ALTER TABLE artifacts
    ADD COLUMN IF NOT EXISTS parquet_row_idx BIGINT;

CREATE INDEX IF NOT EXISTS idx_artifacts_parquet_file
    ON artifacts (parquet_file_id)
    WHERE parquet_file_id IS NOT NULL;
