DROP INDEX IF EXISTS idx_artifacts_parquet_file;
ALTER TABLE artifacts DROP COLUMN IF EXISTS parquet_row_idx;
ALTER TABLE artifacts DROP COLUMN IF EXISTS parquet_file_id;
DROP INDEX IF EXISTS idx_parquet_files_tenant_created;
DROP TABLE IF EXISTS parquet_files;
