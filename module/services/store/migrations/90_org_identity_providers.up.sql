-- Per-organization identity provider registry (P4-SSO-001, issue #107).
--
-- One row per org configures that org's own IdP so a single deployment can
-- host many tenants' providers side by side. Orgs without a row fall back to
-- the global IDENTITY_* provider. The provider name recorded in
-- user_identities.provider is derived per org ("oidc:<org_id>") so identical
-- subjects from different tenants' IdPs never collide.
--
-- client_secret_ref holds a SecretCipher envelope (cfs1:vault-transit:...), a
-- reference into the secret provider — never a plaintext client secret.
--
-- Control-plane authority, RLS like other org-scoped config: own-org access is
-- gated by app.current_org_id; the unauthenticated pre-auth discovery lookups
-- (email domain, vanity host) run through the audited control-plane role.

CREATE TABLE IF NOT EXISTS org_identity_providers (
    id                    UUID DEFAULT gen_random_uuid() PRIMARY KEY,
    org_id                UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    kind                  TEXT NOT NULL CHECK (kind IN ('oidc', 'header-jwt')),
    display_name          TEXT NOT NULL,
    issuer                TEXT,
    client_id             TEXT,
    client_secret_ref     TEXT,
    jwks_url              TEXT,
    token_url             TEXT,
    audience              TEXT,
    claim_map             JSONB  NOT NULL DEFAULT '{}'::jsonb,
    allowed_email_domains TEXT[] NOT NULL DEFAULT '{}',
    allowed_groups        TEXT[] NOT NULL DEFAULT '{}',
    vanity_host           TEXT,
    status                TEXT NOT NULL DEFAULT 'pending'
                              CHECK (status IN ('pending', 'active', 'disabled')),
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (org_id)
);

-- Pre-auth email-domain discovery hot path: an active provider is matched by
-- containment (allowed_email_domains @> ARRAY[domain]).
CREATE INDEX IF NOT EXISTS idx_org_identity_providers_email_domains
    ON org_identity_providers USING GIN (allowed_email_domains);

-- Host-based routing: a vanity host maps to exactly one org, deployment-wide.
-- The unique index is a table constraint, so it holds across tenants
-- independently of RLS.
CREATE UNIQUE INDEX IF NOT EXISTS idx_org_identity_providers_vanity_host
    ON org_identity_providers (vanity_host)
    WHERE vanity_host IS NOT NULL;

-- RLS: same direct-org_id recipe as migration 29. Own-org rows are visible
-- when app.current_org_id matches; cross-tenant workers and the pre-auth
-- discovery path use the bypass/control-plane role.
ALTER TABLE org_identity_providers ENABLE ROW LEVEL SECURITY;
ALTER TABLE org_identity_providers FORCE  ROW LEVEL SECURITY;
CREATE POLICY org_identity_providers_tenant ON org_identity_providers
    USING (
        org_id::text = current_setting('app.current_org_id', true)
        OR current_setting('app.bypass', true) = '1'
    )
    WITH CHECK (
        org_id::text = current_setting('app.current_org_id', true)
        OR current_setting('app.bypass', true) = '1'
    );

-- Exact app_tenant grants (migration 63 convention): request traffic may read,
-- create, and update its own org's provider. Disable is a status update, not a
-- delete, so no DELETE grant is added.
REVOKE ALL PRIVILEGES ON org_identity_providers FROM app_tenant;
GRANT SELECT, INSERT, UPDATE ON org_identity_providers TO app_tenant;

-- The control plane performs the cross-tenant pre-auth discovery reads and owns
-- any retention deletes, so it holds the full DML set new tables require
-- (migration 67 convention; default privileges grant it nothing).
GRANT SELECT, INSERT, UPDATE, DELETE ON org_identity_providers TO app_control_plane;
