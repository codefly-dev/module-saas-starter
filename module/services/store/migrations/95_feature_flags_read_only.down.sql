REVOKE ALL PRIVILEGES ON feature_flags FROM app_tenant;
REVOKE ALL PRIVILEGES ON feature_flags FROM app_control_plane;

GRANT SELECT, INSERT, UPDATE ON feature_flags TO app_tenant;
GRANT SELECT, INSERT, UPDATE, DELETE ON feature_flags TO app_control_plane;
