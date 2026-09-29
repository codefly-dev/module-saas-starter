-- WARNING — this is a security reduction, not a safe rollback.
--
-- Clearing `security_invoker` returns these three views to executing as their
-- OWNER. Wherever that owner is a superuser — which the migration principal is
-- on the provisioned local profile — forced row-level security does not apply
-- to it, so every session granted the view, `app_tenant` included, reads EVERY
-- tenant's delegation rows again whatever scope it bound. That is the crossing
-- 14_delegation_views_security_invoker.up.sql exists to close, and running this
-- reopens it.
--
-- Apply it only to return a database to a ledger that predates that migration.
-- Do not run it to "fix" a read that came back empty: an empty read under
-- invoker execution means the selecting session lacks either the bound scope or
-- the privilege on the base table, and both are fixed where they are wrong.
ALTER VIEW public.delegation_grants_recent SET (security_invoker = false);
ALTER VIEW public.delegation_pattern_usage SET (security_invoker = false);
ALTER VIEW public.delegation_stats_daily SET (security_invoker = false);

COMMENT ON VIEW public.delegation_grants_recent IS 'M9 audit view: last 7 days of delegation grants with actor/grantor names denormalized. Inherits RLS from delegation_grants.';
COMMENT ON VIEW public.delegation_pattern_usage IS 'M9 audit view: pattern-grant burn-rate (use_count / max_uses). usage_pct is NULL when max_uses is null/zero.';
COMMENT ON VIEW public.delegation_stats_daily IS 'M9 audit view: 90-day delegation counts grouped by day/status/risk. auto_approved_count splits out via_pattern grants.';
