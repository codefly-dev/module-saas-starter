-- Persist structured authentication evidence across refresh rotation and add
-- replica-safe attempt locking to the one-use MFA login hand-off.

ALTER TABLE sessions
    ADD COLUMN authentication_methods TEXT[] NOT NULL DEFAULT ARRAY[]::TEXT[],
    ADD COLUMN auth_time TIMESTAMPTZ,
    ADD COLUMN assurance_level TEXT NOT NULL DEFAULT 'aal1',
    ADD COLUMN mfa_verified_at TIMESTAMPTZ,
    ADD CONSTRAINT sessions_assurance_level_check
        CHECK (assurance_level IN ('aal1', 'aal2'));

ALTER TABLE mfa_login_transactions
    ADD COLUMN authentication_methods TEXT[] NOT NULL DEFAULT ARRAY[]::TEXT[],
    ADD COLUMN auth_time TIMESTAMPTZ,
    ADD COLUMN failed_attempts SMALLINT NOT NULL DEFAULT 0,
    ADD COLUMN max_attempts SMALLINT NOT NULL DEFAULT 5,
    ADD COLUMN locked_until TIMESTAMPTZ,
    ADD CONSTRAINT mfa_login_failed_attempts_check CHECK (failed_attempts >= 0),
    ADD CONSTRAINT mfa_login_max_attempts_check CHECK (max_attempts BETWEEN 1 AND 20);
