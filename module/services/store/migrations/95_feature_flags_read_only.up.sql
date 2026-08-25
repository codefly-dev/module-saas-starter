-- The database-backed feature flag catalog is a migration inventory only.
-- Keep reads available to the existing platform UI/export path, but make a
-- missed application-layer guard unable to mutate it through either runtime
-- role.
REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON feature_flags FROM app_tenant;
REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON feature_flags FROM app_control_plane;

GRANT SELECT ON feature_flags TO app_tenant;
GRANT SELECT ON feature_flags TO app_control_plane;
