CREATE TABLE IF NOT EXISTS audit_log (
    id             BIGSERIAL   PRIMARY KEY,
    timestamp      TIMESTAMPTZ NOT NULL DEFAULT now(),
    actor          TEXT        NOT NULL,
    action         TEXT        NOT NULL,
    tenant_id      TEXT        NOT NULL,
    artifact_id    UUID,
    scope          TEXT,
    reason         TEXT,
    before_state   TEXT,
    after_state    TEXT
);

CREATE INDEX IF NOT EXISTS idx_audit_tenant
    ON audit_log (tenant_id, timestamp DESC);

CREATE INDEX IF NOT EXISTS idx_audit_action
    ON audit_log (action, timestamp DESC);
