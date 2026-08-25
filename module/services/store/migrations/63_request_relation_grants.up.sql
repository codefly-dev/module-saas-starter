-- Exact app_tenant grants for every tenant- and user-scoped relation.
-- Forced RLS determines which rows are visible; these grants independently
-- determine which operations request traffic can attempt at all.

REVOKE ALL PRIVILEGES ON
    api_keys,
    audit_events,
    audit_export_configs,
    entitlement_overrides,
    gdpr_requests,
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
FROM app_tenant;

-- Tenant-scoped request relations.
GRANT SELECT, INSERT, UPDATE ON
    api_keys,
    entitlement_overrides,
    invitations,
    org_settings,
    organizations,
    principals,
    subscriptions,
    usage_totals,
    webhook_deliveries
TO app_tenant;

GRANT SELECT, INSERT ON
    audit_events,
    role_permissions,
    usage_events
TO app_tenant;

GRANT SELECT, INSERT, DELETE ON
    role_assignments,
    roles,
    team_members
TO app_tenant;

GRANT SELECT, INSERT, UPDATE, DELETE ON
    audit_export_configs,
    organization_members,
    teams,
    webhook_subscriptions
TO app_tenant;

-- User-scoped request relations.
GRANT SELECT, INSERT, UPDATE ON
    gdpr_requests,
    onboarding_progress,
    sessions,
    user_identities,
    users,
    webauthn_ceremonies,
    webauthn_credentials
TO app_tenant;

GRANT SELECT, INSERT, UPDATE, DELETE ON
    mfa_backup_codes,
    mfa_devices,
    notifications
TO app_tenant;

-- A user-scoped request may create an MFA handoff, but token-based reads and
-- updates use the audited pre-auth/control-plane path.
GRANT INSERT ON mfa_login_transactions TO app_tenant;

-- magic_links is exclusively pre-auth/control-plane. Retention deletes for
-- audit_events, sessions, webhook_deliveries, authentication ceremonies, and
-- other user data also run outside app_tenant, so no DELETE grant is added for
-- those relations.
