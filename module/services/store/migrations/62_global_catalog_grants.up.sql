-- Exact app_tenant grants for relations that intentionally have no tenant RLS.
-- Handler authorization remains necessary, but a missed handler gate must not
-- turn broad historical CRUD grants into catalog deletion or billing mutation.

REVOKE ALL PRIVILEGES ON
    identity_providers,
    plans,
    plan_entitlements,
    feature_flags,
    email_templates,
    data_retention_policies,
    bootstrap_state,
    platform_admins
FROM app_tenant;

-- Immutable/reference catalogs on request paths.
GRANT SELECT ON
    identity_providers,
    plans,
    plan_entitlements,
    email_templates,
    data_retention_policies
TO app_tenant;

-- The authentication resolver atomically claims the singleton bootstrap row.
GRANT SELECT, UPDATE ON bootstrap_state TO app_tenant;

-- These writes are platform-super-admin commands in the application layer.
GRANT SELECT, INSERT, UPDATE ON feature_flags TO app_tenant;
GRANT SELECT, INSERT, UPDATE, DELETE ON platform_admins TO app_tenant;
