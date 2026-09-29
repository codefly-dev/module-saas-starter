-- A view that is not `security_invoker` executes as its OWNER, not as the
-- session selecting it. These three read `delegation_grants` (and two of them
-- join `principals`), both of which force row-level security, and both of whose
-- tenant policies — `delegation_grants_tenant`, `principals_access` — carry no
-- TO clause and so apply to every role. The views' own comments say they
-- "inherit RLS from delegation_grants". Under owner execution they do not:
-- forced row-level security does not apply to a superuser, so wherever the
-- migration principal that created them is a superuser, every session selecting
-- one reads EVERY tenant's rows whatever scope it bound.
--
-- The exposure is not confined to a read-only capability. All three are granted
-- to `app_tenant` (SELECT, INSERT, DELETE, UPDATE, from the baseline), and
-- `app_tenant` is the only role the request login reaches — so this crosses
-- tenants on ordinary request traffic, which is the boundary the split login is
-- for. Measured on a local provisioned store with no tenant scope bound:
-- `delegation_grants` returned 0 rows, as its forced policy requires, while
-- these three returned 114, 108 and 36.
--
-- `security_invoker` makes each view execute as the session selecting it, so the
-- base tables' own policies apply to that session. Nothing else changes: no
-- grant is added or removed, no owner is swapped, and no base-table policy is
-- touched. Invoker execution requires the selecting role to hold the privileges
-- the view reads through, which the roles that legitimately read these already
-- do — `app_tenant` holds SELECT on `delegation_grants`, `principals` and
-- `organization_members` (the last reached by `principals_access`'s EXISTS), and
-- the read-only capability is provisioned with SELECT on the application
-- relations. `app_control_plane` keeps its cross-tenant reach through its own
-- `app_control_plane_explicit_rows` policy, which it satisfies only once it has
-- actually assumed the role.
--
-- Requires PostgreSQL 15 or later; this ledger's floor is 16.
ALTER VIEW public.delegation_grants_recent SET (security_invoker = true);
ALTER VIEW public.delegation_pattern_usage SET (security_invoker = true);
ALTER VIEW public.delegation_stats_daily SET (security_invoker = true);

-- The baseline comments assert an inheritance that only holds from here on.
-- Restate them so the schema does not keep claiming a property it lacked.
COMMENT ON VIEW public.delegation_grants_recent IS 'M9 audit view: last 7 days of delegation grants with actor/grantor names denormalized. security_invoker: executes as the selecting session, so delegation_grants and principals apply their own row-level security to it.';
COMMENT ON VIEW public.delegation_pattern_usage IS 'M9 audit view: pattern-grant burn-rate (use_count / max_uses). usage_pct is NULL when max_uses is null/zero. security_invoker: executes as the selecting session, so delegation_grants and principals apply their own row-level security to it.';
COMMENT ON VIEW public.delegation_stats_daily IS 'M9 audit view: 90-day delegation counts grouped by day/status/risk. auto_approved_count splits out via_pattern grants. security_invoker: executes as the selecting session, so delegation_grants applies its own row-level security to it.';
