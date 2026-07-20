-- WebAuthn credentials are MFA devices with authenticator-specific state.
-- Only the public credential ID remains queryable; the complete credential
-- record and short-lived ceremony session are encrypted with Vault Transit.

ALTER TABLE mfa_devices
    ALTER COLUMN secret_encrypted DROP NOT NULL;

ALTER TABLE mfa_devices
    ADD CONSTRAINT mfa_devices_secret_by_type CHECK (
        (device_type = 'totp' AND secret_encrypted IS NOT NULL)
        OR (device_type = 'webauthn' AND secret_encrypted IS NULL)
    );

CREATE TABLE webauthn_credentials (
    device_id             UUID PRIMARY KEY REFERENCES mfa_devices(id) ON DELETE CASCADE,
    user_id               UUID NOT NULL REFERENCES users(uuid) ON DELETE CASCADE,
    credential_id         BYTEA NOT NULL UNIQUE CHECK (octet_length(credential_id) > 0),
    credential_encrypted  TEXT NOT NULL,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_webauthn_credentials_user
    ON webauthn_credentials(user_id);

CREATE TABLE webauthn_ceremonies (
    id                        UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    token_hash                TEXT NOT NULL UNIQUE CHECK (length(token_hash) = 64),
    user_id                   UUID NOT NULL REFERENCES users(uuid) ON DELETE CASCADE,
    mfa_login_transaction_id  UUID REFERENCES mfa_login_transactions(id) ON DELETE CASCADE,
    ceremony_type             TEXT NOT NULL CHECK (ceremony_type IN ('registration', 'login')),
    session_data_encrypted    TEXT NOT NULL,
    expires_at                TIMESTAMPTZ NOT NULL,
    consumed_at               TIMESTAMPTZ,
    created_at                TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT webauthn_ceremony_owner CHECK (
        (ceremony_type = 'registration' AND mfa_login_transaction_id IS NULL)
        OR (ceremony_type = 'login' AND mfa_login_transaction_id IS NOT NULL)
    )
);

CREATE INDEX idx_webauthn_ceremonies_user_active
    ON webauthn_ceremonies(user_id, expires_at)
    WHERE consumed_at IS NULL;

CREATE INDEX idx_webauthn_ceremonies_expiry
    ON webauthn_ceremonies(expires_at)
    WHERE consumed_at IS NULL;

ALTER TABLE webauthn_credentials ENABLE ROW LEVEL SECURITY;
ALTER TABLE webauthn_credentials FORCE ROW LEVEL SECURITY;
CREATE POLICY webauthn_credentials_user ON webauthn_credentials
    USING (
        current_setting('app.bypass', true) = '1'
        OR user_id::text = current_setting('app.current_user_id', true)
    )
    WITH CHECK (
        current_setting('app.bypass', true) = '1'
        OR user_id::text = current_setting('app.current_user_id', true)
    );

ALTER TABLE webauthn_ceremonies ENABLE ROW LEVEL SECURITY;
ALTER TABLE webauthn_ceremonies FORCE ROW LEVEL SECURITY;
CREATE POLICY webauthn_ceremonies_user ON webauthn_ceremonies
    USING (
        current_setting('app.bypass', true) = '1'
        OR user_id::text = current_setting('app.current_user_id', true)
    )
    WITH CHECK (
        current_setting('app.bypass', true) = '1'
        OR user_id::text = current_setting('app.current_user_id', true)
    );
