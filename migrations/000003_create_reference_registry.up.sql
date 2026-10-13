CREATE TABLE IF NOT EXISTS reference_registry (
    tenant_id    TEXT        NOT NULL,
    digest      TEXT        NOT NULL,
    owner_id    TEXT        NOT NULL,
    owner_type  TEXT        NOT NULL DEFAULT 'unknown',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    released_at TIMESTAMPTZ,
    PRIMARY KEY (tenant_id, digest, owner_id),
    FOREIGN KEY (tenant_id, digest) REFERENCES artifacts (tenant_id, digest) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_reference_registry_tenant_digest
    ON reference_registry (tenant_id, digest);

CREATE INDEX IF NOT EXISTS idx_reference_registry_owner
    ON reference_registry (tenant_id, owner_id);
