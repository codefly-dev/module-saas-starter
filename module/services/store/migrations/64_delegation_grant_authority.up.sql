-- Delegation grants are a tenant-scoped state machine. Requests may create,
-- inspect, and transition grants, but there is no request-path hard delete.

REVOKE ALL PRIVILEGES ON delegation_grants FROM app_tenant;
GRANT SELECT, INSERT, UPDATE ON delegation_grants TO app_tenant;
