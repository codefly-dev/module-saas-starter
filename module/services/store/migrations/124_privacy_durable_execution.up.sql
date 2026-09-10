-- Privacy export/deletion requests run on the durable jobs platform instead of
-- a detached goroutine. The request row is the product-visible projection of
-- that execution: the job that owns it, the worker lease that fences its
-- transitions, the per-step provider receipts a retry reads before repeating an
-- external effect, and a bounded operator-safe failure code.

ALTER TABLE gdpr_requests
    ADD COLUMN job_id        UUID,
    ADD COLUMN lease_owner   TEXT,
    ADD COLUMN lease_token   UUID,
    ADD COLUMN attempt_count INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN failure_code  TEXT,
    ADD COLUMN step_receipts JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN updated_at    TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP;

-- Requests accepted before durable execution have no job, so no worker will
-- ever claim them, and whether their adapter already produced an external
-- effect is unknown. Park them in a terminal state that names them for operator
-- review rather than replaying effects that may already have happened.
UPDATE gdpr_requests
SET status       = 'failed',
    failure_code = 'privacy.pre_durable_request',
    error        = COALESCE(
        NULLIF(error, ''),
        'Accepted before durable privacy execution; operator review required before any replay.'
    ),
    updated_at   = CURRENT_TIMESTAMP
WHERE status IN ('pending', 'processing');

ALTER TABLE gdpr_requests DROP CONSTRAINT gdpr_requests_status_check;

ALTER TABLE gdpr_requests
    ADD CONSTRAINT gdpr_requests_status_check CHECK (
        status IN ('pending', 'processing', 'retrying', 'completed', 'failed')
    ),
    -- Every request that can still progress is owned by a durable job; only
    -- history written before this migration may lack one.
    ADD CONSTRAINT gdpr_requests_job_required CHECK (
        job_id IS NOT NULL OR status IN ('completed', 'failed')
    ),
    ADD CONSTRAINT gdpr_requests_lease_check CHECK (
        (lease_owner IS NULL) = (lease_token IS NULL)
        AND (lease_owner IS NULL OR length(lease_owner) BETWEEN 1 AND 255)
    ),
    ADD CONSTRAINT gdpr_requests_failure_code_check CHECK (
        failure_code IS NULL OR failure_code ~ '^[a-z][a-z0-9_.-]{0,127}$'
    ),
    ADD CONSTRAINT gdpr_requests_attempt_check CHECK (attempt_count >= 0),
    ADD CONSTRAINT gdpr_requests_step_receipts_check CHECK (
        jsonb_typeof(step_receipts) = 'object'
        AND octet_length(step_receipts::text) <= 16384
    );

-- A deletion workflow's own record has to outlive the subject it removes: the
-- request is durable evidence about a person, like an audit event, not a row
-- that belongs to them. A referencing key would force an adapter that removes
-- the user row to destroy the workflow record first, leaving nothing to finish
-- from or report against. The user-scoped RLS policy still binds a new request
-- to its authenticated caller, so a request cannot name a subject at will.
ALTER TABLE gdpr_requests DROP CONSTRAINT gdpr_requests_user_id_fkey;

CREATE UNIQUE INDEX uq_gdpr_requests_job
    ON gdpr_requests(job_id)
    WHERE job_id IS NOT NULL;

-- Request traffic accepts and reads privacy requests; only the leased worker
-- transitions one. Removing UPDATE from the request role means a caller cannot
-- reach execution state at all, let alone finalize somebody's work.
REVOKE UPDATE ON gdpr_requests FROM app_tenant;
