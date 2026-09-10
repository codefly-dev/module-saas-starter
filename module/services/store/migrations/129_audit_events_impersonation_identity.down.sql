DROP INDEX IF EXISTS idx_audit_events_impersonated_by_time;

ALTER TABLE audit_events
    DROP CONSTRAINT IF EXISTS audit_events_impersonation_identity_complete;

ALTER TABLE audit_events
    DROP COLUMN IF EXISTS is_impersonated,
    DROP COLUMN IF EXISTS impersonated_by;
