CREATE TABLE IF NOT EXISTS retention_policies (
    tenant_id      TEXT        NOT NULL,
    artifact_id    UUID        NOT NULL,
    owner_id       TEXT        NOT NULL,
    retention_days INTEGER     NOT NULL,
    expires_at     TIMESTAMPTZ NOT NULL,
    legal_hold     BOOLEAN     NOT NULL DEFAULT false,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, artifact_id, owner_id),
    FOREIGN KEY (tenant_id, artifact_id) REFERENCES artifacts (tenant_id, artifact_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_retention_expires
    ON retention_policies (expires_at)
    WHERE legal_hold = false;
