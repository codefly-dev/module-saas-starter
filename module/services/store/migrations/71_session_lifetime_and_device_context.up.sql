-- Make idle expiry explicit on every refresh session and preserve per-device
-- display context across the durable one-use MFA login handoff. Device context
-- is never an authentication or authorization input.

ALTER TABLE sessions
    ADD COLUMN idle_expires_at TIMESTAMPTZ;

UPDATE sessions
SET idle_expires_at = LEAST(expires_at, last_active_at + INTERVAL '24 hours');

ALTER TABLE sessions
    ALTER COLUMN idle_expires_at SET NOT NULL;

ALTER TABLE mfa_login_transactions
    ADD COLUMN device_info JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN ip_address TEXT;
