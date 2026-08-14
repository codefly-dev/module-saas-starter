-- Per-org JIT provisioning policy for enterprise SSO. When an org brings its
-- own IdP, that IdP is the source of truth for who may log in, so a first-seen
-- identity from the org-bound provider must be provisioned into THIS org on
-- first login rather than self-signing-up or creating an org. The global
-- IDENTITY_SIGNUP_MODE does not fit — provisioning policy is per-org here.
--
-- Columns live on organizations alongside the existing sso_* config (migration
-- 21): each org has at most one SSO provider, so a companion table would only
-- add a join. sso_provision_mode NULL means no policy is configured and the
-- identity resolver keeps its existing intent selection for that org.
ALTER TABLE organizations
    ADD COLUMN IF NOT EXISTS sso_provision_mode        TEXT
        CHECK (sso_provision_mode IN ('jit', 'invite-only', 'disabled')),
    ADD COLUMN IF NOT EXISTS sso_default_role          TEXT NOT NULL DEFAULT 'member'
        CHECK (sso_default_role IN ('member', 'admin')),
    ADD COLUMN IF NOT EXISTS sso_allowed_email_domains TEXT[] NOT NULL DEFAULT '{}';
