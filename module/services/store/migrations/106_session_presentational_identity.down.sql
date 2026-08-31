ALTER TABLE mfa_login_transactions
    DROP COLUMN IF EXISTS display_name,
    DROP COLUMN IF EXISTS email;

ALTER TABLE sessions
    DROP COLUMN IF EXISTS display_name,
    DROP COLUMN IF EXISTS email;
