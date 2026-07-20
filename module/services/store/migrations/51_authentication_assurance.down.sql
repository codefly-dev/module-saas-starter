ALTER TABLE mfa_login_transactions
    DROP CONSTRAINT IF EXISTS mfa_login_max_attempts_check,
    DROP CONSTRAINT IF EXISTS mfa_login_failed_attempts_check,
    DROP COLUMN IF EXISTS locked_until,
    DROP COLUMN IF EXISTS max_attempts,
    DROP COLUMN IF EXISTS failed_attempts,
    DROP COLUMN IF EXISTS auth_time,
    DROP COLUMN IF EXISTS authentication_methods;

ALTER TABLE sessions
    DROP CONSTRAINT IF EXISTS sessions_assurance_level_check,
    DROP COLUMN IF EXISTS mfa_verified_at,
    DROP COLUMN IF EXISTS assurance_level,
    DROP COLUMN IF EXISTS auth_time,
    DROP COLUMN IF EXISTS authentication_methods;
