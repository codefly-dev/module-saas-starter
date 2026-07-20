-- Restore the broad pre-62 request-role grants.

GRANT SELECT, INSERT, UPDATE, DELETE ON
    identity_providers,
    plans,
    plan_entitlements,
    feature_flags,
    email_templates,
    data_retention_policies,
    bootstrap_state,
    platform_admins
TO app_tenant;
