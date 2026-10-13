CREATE TABLE IF NOT EXISTS artifacts (
    tenant_id         TEXT        NOT NULL,
    artifact_id      UUID        NOT NULL,
    digest           TEXT        NOT NULL,
    generation       INTEGER     NOT NULL DEFAULT 1,
    serializer_version INTEGER   NOT NULL DEFAULT 1,
    content_type     TEXT,
    size_bytes       BIGINT      NOT NULL DEFAULT 0,
    storage_tier     TEXT        NOT NULL DEFAULT 'inline',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at       TIMESTAMPTZ,
    PRIMARY KEY (tenant_id, artifact_id)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_artifacts_tenant_digest
    ON artifacts (tenant_id, digest);

CREATE INDEX IF NOT EXISTS idx_artifacts_tenant_created
    ON artifacts (tenant_id, created_at DESC);
