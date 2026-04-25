-- Per-org audit log export configs. The exporter goroutine polls this
-- table on a 1-min cycle, finds rows where last_exported_at is older
-- than now() - cadence_seconds, and uploads new audit events to the
-- configured bucket as JSONL.
--
-- Credentials live here on purpose: rotated by the customer through
-- the admin UI, encrypted at rest at the database level, never
-- shipped to the FE in clear (returns "" for the secret_access_key
-- on List/Get so the FE never displays it). For SSE-side encryption
-- we use the storage backend's bucket-level KMS — no per-row crypto
-- here, that's the customer's bucket policy to enforce.

CREATE TABLE IF NOT EXISTS audit_export_configs (
    id              UUID PRIMARY KEY,
    org_id          UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    -- bucket / region / endpoint cover both AWS S3 (endpoint nullable
    -- → defaults to AWS) and any S3-compatible store (R2, MinIO, GCS
    -- in S3-compat mode, etc.) — endpoint is the override knob.
    bucket          TEXT NOT NULL,
    region          TEXT NOT NULL DEFAULT 'us-east-1',
    endpoint        TEXT,                            -- null = AWS S3
    -- Object key prefix inside the bucket. Empty = bucket root.
    -- Exporter appends `<yyyy-mm-dd>/<run-id>.jsonl` per export.
    prefix          TEXT NOT NULL DEFAULT '',
    -- Credentials. Stored cleartext in DB; bucket-level encryption
    -- (Postgres TDE / disk encryption) is the layer that protects
    -- them at rest. List/Get RPCs return "" for secret_access_key.
    access_key_id     TEXT NOT NULL,
    secret_access_key TEXT NOT NULL,
    -- Cadence: minutes between exports. 60 = hourly, 1440 = daily.
    -- Bound to >= 5 to avoid hammering the bucket.
    cadence_minutes INT NOT NULL DEFAULT 60 CHECK (cadence_minutes >= 5),
    enabled         BOOLEAN NOT NULL DEFAULT TRUE,
    last_exported_at TIMESTAMPTZ,
    -- last_error captures the most recent failure for surfacing in
    -- the admin UI ("retrying — last attempt 3m ago: 403 access
    -- denied"). Cleared on successful export.
    last_error      TEXT,
    last_error_at   TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- One config per org, full stop. Multi-bucket-per-org is YAGNI;
    -- if a customer wants two destinations, they fan out from theirs.
    UNIQUE (org_id)
);

CREATE INDEX IF NOT EXISTS idx_audit_export_configs_enabled_due
    ON audit_export_configs (last_exported_at NULLS FIRST)
    WHERE enabled;
