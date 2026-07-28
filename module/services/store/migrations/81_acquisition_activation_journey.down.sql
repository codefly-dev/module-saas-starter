DROP TABLE IF EXISTS user_consent_events;
DROP TABLE IF EXISTS user_consent_preferences;
DROP TABLE IF EXISTS waitlist_entries;
DROP TABLE IF EXISTS organization_activations;

DROP INDEX IF EXISTS onboarding_progress_scoped_unique;
DROP INDEX IF EXISTS onboarding_progress_legacy_unique;

DELETE FROM onboarding_progress WHERE org_id IS NOT NULL;

ALTER TABLE onboarding_progress
    DROP COLUMN IF EXISTS skip_reason,
    DROP COLUMN IF EXISTS completion_method,
    DROP COLUMN IF EXISTS skipped_at,
    DROP COLUMN IF EXISTS last_seen_at,
    DROP COLUMN IF EXISTS started_at,
    DROP COLUMN IF EXISTS first_seen_at,
    DROP COLUMN IF EXISTS required,
    DROP COLUMN IF EXISTS persona,
    DROP COLUMN IF EXISTS audience,
    DROP COLUMN IF EXISTS variant,
    DROP COLUMN IF EXISTS flow_version,
    DROP COLUMN IF EXISTS flow_id,
    DROP COLUMN IF EXISTS org_id;

ALTER TABLE onboarding_progress
    ADD CONSTRAINT onboarding_progress_user_id_step_name_key UNIQUE (user_id, step_name);

ALTER TABLE invitations
    DROP COLUMN IF EXISTS send_count,
    DROP COLUMN IF EXISTS last_sent_at,
    DROP COLUMN IF EXISTS delivery_status,
    DROP COLUMN IF EXISTS inviter_display_name;
