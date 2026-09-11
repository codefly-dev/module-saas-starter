GRANT UPDATE ON gdpr_requests TO app_tenant;

DROP INDEX IF EXISTS uq_gdpr_requests_job;

-- Restoring the referencing key means restoring what it asserted: a request
-- whose subject row is already gone could not exist under the old schema. Those
-- rows are exactly the completed-deletion records — the durable evidence that
-- an erasure happened — so they are preserved verbatim before the constraint
-- forces them out, rather than destroyed by a rollback.
CREATE TABLE IF NOT EXISTS gdpr_requests_preserved_on_rollback
    (LIKE gdpr_requests INCLUDING DEFAULTS);

INSERT INTO gdpr_requests_preserved_on_rollback
SELECT * FROM gdpr_requests
WHERE NOT EXISTS (SELECT 1 FROM users WHERE users.uuid = gdpr_requests.user_id);

DELETE FROM gdpr_requests
WHERE NOT EXISTS (SELECT 1 FROM users WHERE users.uuid = gdpr_requests.user_id);

ALTER TABLE gdpr_requests
    ADD CONSTRAINT gdpr_requests_user_id_fkey
        FOREIGN KEY (user_id) REFERENCES users(uuid);

ALTER TABLE gdpr_requests
    DROP CONSTRAINT IF EXISTS gdpr_requests_step_receipts_check,
    DROP CONSTRAINT IF EXISTS gdpr_requests_attempt_check,
    DROP CONSTRAINT IF EXISTS gdpr_requests_failure_code_check,
    DROP CONSTRAINT IF EXISTS gdpr_requests_lease_check,
    DROP CONSTRAINT IF EXISTS gdpr_requests_job_required,
    DROP CONSTRAINT IF EXISTS gdpr_requests_status_check;

UPDATE gdpr_requests SET status = 'processing' WHERE status = 'retrying';

ALTER TABLE gdpr_requests
    ADD CONSTRAINT gdpr_requests_status_check CHECK (
        status IN ('pending', 'processing', 'completed', 'failed')
    );

ALTER TABLE gdpr_requests
    DROP COLUMN step_receipts,
    DROP COLUMN failure_code,
    DROP COLUMN attempt_count,
    DROP COLUMN lease_token,
    DROP COLUMN lease_owner,
    DROP COLUMN job_id,
    DROP COLUMN updated_at;
