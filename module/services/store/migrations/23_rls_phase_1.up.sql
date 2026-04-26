-- Postgres RLS — Phase 1
--
-- Enables row-level security on audit_export_configs only. This is
-- the simplest per-tenant table to land first:
--   * Every Store method takes orgID explicitly (no bare-id lookups
--     that need an extra WithBypass→orgID resolve dance).
--   * Every business.Service consumer is now wrapped in WithOrgTx
--     for request-path code or WithBypass for the audit-exporter
--     goroutine's cross-tenant scan.
--
-- Phase 2+ extends RLS to webhook_subscriptions, webhook_deliveries,
-- api_keys, teams, invitations, organization_members, subscriptions,
-- entitlement_overrides, usage_records, audit_events, roles,
-- role_assignments, org_settings (see RLS_PLAN.md).
--
-- Companion migration 24 creates the `app_tenant` role that
-- WithOrgTx SET LOCAL ROLE-switches to. Without it, queries hit RLS
-- as a superuser and bypass the policy.
--
-- ──────────────────────────────────────────────────────────────────
--
-- Policy semantics: read two GUCs that the api sets via SET LOCAL
-- inside a transaction:
--   app.current_org_id  — set by infra.WithOrgTx(ctx, orgID, fn)
--                         (request-path code)
--   app.bypass          — set by infra.WithBypass(ctx, fn)
--                         [reserved — workers currently bypass via
--                          the default superuser role; this GUC
--                          becomes load-bearing the day we drop
--                          superuser from the worker connection]
--
-- Default for both (when not set): "" — current_setting(..., true)
-- returns empty string instead of erroring. So a query that runs as
-- app_tenant WITHOUT either wrapper returns ZERO ROWS — fail-closed.
--
-- ENABLE + FORCE: ENABLE makes non-owner queries subject to the
-- policy; FORCE applies the policy to the table OWNER too. FORCE is
-- the load-bearing word.

ALTER TABLE audit_export_configs ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit_export_configs FORCE  ROW LEVEL SECURITY;

CREATE POLICY audit_export_configs_tenant ON audit_export_configs
    USING (
        org_id::text = current_setting('app.current_org_id', true)
        OR current_setting('app.bypass', true) = '1'
    )
    WITH CHECK (
        org_id::text = current_setting('app.current_org_id', true)
        OR current_setting('app.bypass', true) = '1'
    );

-- Operator audit query:
--
--   SELECT relname, relrowsecurity, relforcerowsecurity
--     FROM pg_class WHERE relname = 'audit_export_configs';
--
-- Both columns must be true after this migration.
