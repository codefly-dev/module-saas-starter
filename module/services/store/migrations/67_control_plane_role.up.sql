-- Named authority for pre-authentication, bootstrap, platform administration,
-- retention, and other deliberately cross-tenant account operations.
--
-- Unlike the old app.bypass custom setting, this capability cannot be minted
-- by arbitrary SQL. The connection principal must have explicit SET
-- membership in this NOLOGIN role. BYPASSRLS is deliberately paired with
-- bounded DML: no ownership, DDL, TRUNCATE, temporary objects, or worker inbox.

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'app_control_plane') THEN
        CREATE ROLE app_control_plane
            NOLOGIN NOINHERIT NOSUPERUSER BYPASSRLS
            NOCREATEDB NOCREATEROLE NOREPLICATION;
    ELSE
        ALTER ROLE app_control_plane
            NOLOGIN NOINHERIT NOSUPERUSER BYPASSRLS
            NOCREATEDB NOCREATEROLE NOREPLICATION;
    END IF;
END $$;

GRANT USAGE ON SCHEMA public TO app_control_plane;
REVOKE CREATE ON SCHEMA public FROM app_control_plane;

-- Reconcile instead of accumulating historical authority.
REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public FROM app_control_plane;
REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public FROM app_control_plane;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
    REVOKE ALL ON TABLES FROM app_control_plane;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
    REVOKE ALL ON SEQUENCES FROM app_control_plane;

-- The account control plane may perform application DML across RLS scopes.
-- Generic job tables remain exclusive to app_job_worker.
GRANT SELECT, INSERT, UPDATE, DELETE ON
    api_keys,
    audit_events,
    bootstrap_state,
    data_retention_policies,
    delegation_grants,
    email_templates,
    entitlement_overrides,
    feature_flags,
    gdpr_requests,
    identity_providers,
    invitations,
    magic_links,
    mfa_backup_codes,
    mfa_devices,
    mfa_login_transactions,
    notifications,
    onboarding_progress,
    org_settings,
    organization_members,
    organizations,
    plan_entitlements,
    plans,
    platform_admins,
    principals,
    role_assignments,
    role_permissions,
    roles,
    sessions,
    subscriptions,
    team_members,
    teams,
    usage_events,
    usage_totals,
    user_identities,
    users,
    webauthn_ceremonies,
    webauthn_credentials,
    webhook_deliveries,
    webhook_subscriptions
TO app_control_plane;

DO $$
DECLARE
    cur TEXT := current_user;
BEGIN
    EXECUTE format(
        'REVOKE TEMPORARY, CONNECT ON DATABASE %I FROM app_control_plane',
        current_database()
    );
    EXECUTE format('GRANT app_control_plane TO %I', cur);
END $$;
