-- Add TRUNCATE privilege to app_tenant.
--
-- Test cleanup paths (e.g. wipeBootstrapState in
-- bootstrap_admin_test.go) and admin tooling that needs to wipe a
-- single table both need TRUNCATE. Forcing them to escalate to
-- WithBypass for every cleanup is awkward.
--
-- Security trade-off: TRUNCATE is table-level, NOT row-level — RLS
-- doesn't apply. A bug that runs `TRUNCATE webhook_subscriptions`
-- without proper authz would wipe ALL tenants' webhooks at once.
-- L1+L2 (handler authz + RBAC) MUST gate any TRUNCATE call. Audit
-- log + irrecoverability of TRUNCATE makes this a one-shot
-- compliance-loud failure if it ever happens.
--
-- Production code uses DELETE FROM where filtering is needed (see
-- pkg/infra/postgres.go ClearAll comment). TRUNCATE is reserved for
-- test fixtures and explicit "wipe table" admin tools.

GRANT TRUNCATE ON ALL TABLES IN SCHEMA public TO app_tenant;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
    GRANT TRUNCATE ON TABLES TO app_tenant;
