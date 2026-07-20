-- Durable, replica-safe MFA login hand-off.
-- The browser receives only the random token; the database stores its SHA-256
-- hash. A successful factor check consumes the row in the same transaction
-- that inserts the refreshable session.

CREATE TABLE mfa_login_transactions (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    token_hash    TEXT NOT NULL UNIQUE CHECK (length(token_hash) = 64),
    user_id       UUID NOT NULL REFERENCES users(uuid) ON DELETE CASCADE,
    org_id        UUID REFERENCES organizations(id) ON DELETE SET NULL,
    org_role      TEXT NOT NULL DEFAULT '',
    platform_role TEXT NOT NULL DEFAULT '',
    session_id    UUID NOT NULL,
    expires_at    TIMESTAMPTZ NOT NULL,
    consumed_at   TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_mfa_login_transactions_user_active
    ON mfa_login_transactions(user_id, expires_at)
    WHERE consumed_at IS NULL;

CREATE INDEX idx_mfa_login_transactions_expiry
    ON mfa_login_transactions(expires_at)
    WHERE consumed_at IS NULL;

ALTER TABLE mfa_login_transactions ENABLE ROW LEVEL SECURITY;
ALTER TABLE mfa_login_transactions FORCE ROW LEVEL SECURITY;

CREATE POLICY mfa_login_transactions_user ON mfa_login_transactions
    USING (
        current_setting('app.bypass', true) = '1'
        OR user_id::text = current_setting('app.current_user_id', true)
    )
    WITH CHECK (
        current_setting('app.bypass', true) = '1'
        OR user_id::text = current_setting('app.current_user_id', true)
    );

-- Preserve the legacy login gate on refresh rotation. Migration 51 adds the
-- richer amr/auth_time/assurance evidence; this boolean remains during the
-- compatibility window for older consumers.
ALTER TABLE sessions
    ADD COLUMN mfa_satisfied BOOLEAN NOT NULL DEFAULT FALSE;
