ALTER TABLE mfa_login_transactions
    DROP COLUMN IF EXISTS ip_address,
    DROP COLUMN IF EXISTS device_info;

ALTER TABLE sessions
    DROP COLUMN IF EXISTS idle_expires_at;
