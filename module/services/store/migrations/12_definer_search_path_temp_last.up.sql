-- Every SECURITY DEFINER function in the store lists pg_temp last in its
-- search_path. A function-level search_path that does not name pg_temp still
-- searches the calling session's temporary schema, and searches it FIRST, for
-- relation and type names. A caller holding TEMPORARY on the database could then
-- create a temporary table named like a relation the function reads unqualified,
-- and the function would read the caller's table with its owner's authority.
-- Migration 11 closed this for the audit partition functions after reproducing it
-- on PostgreSQL 16; these are the rest. The baseline revokes TEMPORARY from
-- PUBLIC, so no runtime role holds it today; this keeps the functions safe if a
-- later grant, or a login outside the runtime roles, does.
--
-- Each function keeps its schemas in their existing order with pg_temp appended.
-- None of them reads or creates a temporary relation, so every name resolves to
-- what it resolved to before, and only a temporary relation stops shadowing it.
-- The list is the catalog's: every public SECURITY DEFINER function whose
-- search_path did not end in pg_temp on a freshly migrated store.
--
-- ALTER FUNCTION requires ownership. A managed migration principal is a
-- non-superuser CREATEROLE role that may SET the runtime roles but does not
-- inherit them, so it assumes each owning role for that role's functions, and
-- hands the transaction back before the ledger write, which no runtime role may
-- perform.

-- Owned by the migration principal.
ALTER FUNCTION public.append_job_state_transition() SET search_path TO 'pg_catalog', 'public', 'pg_temp';
ALTER FUNCTION public.audit_events_drop_partitions_before(timestamp with time zone) SET search_path TO 'public', 'pg_temp';
ALTER FUNCTION public.record_delivery_event(text,text,text,text,text,timestamp with time zone,uuid) SET search_path TO 'pg_catalog', 'public', 'pg_temp';
ALTER FUNCTION public.sync_webhook_event_subscriptions(uuid,uuid,text[]) SET search_path TO 'pg_catalog', 'public', 'pg_temp';

-- Owned by app_control_plane.
SET LOCAL ROLE app_control_plane;
ALTER FUNCTION public.actor_chain_revocation_bump_authorization() SET search_path TO 'pg_catalog', 'public', 'pg_temp';
ALTER FUNCTION public.authorization_revision_membership_mutation() SET search_path TO 'pg_catalog', 'public', 'pg_temp';
ALTER FUNCTION public.authorization_revision_organization_insert() SET search_path TO 'pg_catalog', 'public', 'pg_temp';
ALTER FUNCTION public.authorization_revision_platform_admin_mutation() SET search_path TO 'pg_catalog', 'public', 'pg_temp';
ALTER FUNCTION public.authorization_revision_principal_mutation() SET search_path TO 'pg_catalog', 'public', 'pg_temp';
ALTER FUNCTION public.authorization_revision_role_assignment_mutation() SET search_path TO 'pg_catalog', 'public', 'pg_temp';
ALTER FUNCTION public.authorization_revision_role_mutation() SET search_path TO 'pg_catalog', 'public', 'pg_temp';
ALTER FUNCTION public.authorization_revision_role_permission_mutation() SET search_path TO 'pg_catalog', 'public', 'pg_temp';
ALTER FUNCTION public.authorization_revision_team_member_mutation() SET search_path TO 'pg_catalog', 'public', 'pg_temp';
ALTER FUNCTION public.authorization_revision_team_mutation() SET search_path TO 'pg_catalog', 'public', 'pg_temp';
ALTER FUNCTION public.authorization_revision_user_mutation() SET search_path TO 'pg_catalog', 'public', 'pg_temp';
ALTER FUNCTION public.bump_organization_authorization_revision(uuid) SET search_path TO 'pg_catalog', 'public', 'pg_temp';
ALTER FUNCTION public.bump_principal_and_organization_authorization(uuid,uuid) SET search_path TO 'pg_catalog', 'public', 'pg_temp';
ALTER FUNCTION public.bump_principal_authorization_revision(uuid,uuid) SET search_path TO 'pg_catalog', 'public', 'pg_temp';
ALTER FUNCTION public.bump_role_authorization_revision(uuid,uuid) SET search_path TO 'pg_catalog', 'public', 'pg_temp';
ALTER FUNCTION public.bump_source_read_revision() SET search_path TO 'pg_catalog', 'public', 'pg_temp';
ALTER FUNCTION public.identity_administered_organizations(uuid) SET search_path TO 'pg_catalog', 'public', 'pg_temp';
ALTER FUNCTION public.invalidate_authorization_sessions() SET search_path TO 'pg_catalog', 'public', 'pg_temp';
ALTER FUNCTION public.invalidate_scoped_role_sessions() SET search_path TO 'pg_catalog', 'public', 'pg_temp';
ALTER FUNCTION public.organization_eligible_administrators(uuid) SET search_path TO 'pg_catalog', 'public', 'pg_temp';
ALTER FUNCTION public.organization_member_primary_email(uuid) SET search_path TO 'pg_catalog', 'public', 'pg_temp';
ALTER FUNCTION public.publish_domain_event(uuid,text,text,text,timestamp with time zone,text,text,text,bytea,uuid,text,text,text,text,text,text,text,integer,bytea) SET search_path TO 'pg_catalog', 'public', 'pg_temp';
ALTER FUNCTION public.sync_human_principal() SET search_path TO 'pg_catalog', 'public', 'pg_temp';
RESET ROLE;

-- Owned by app_job_worker.
SET LOCAL ROLE app_job_worker;
ALTER FUNCTION public.enqueue_job_message(text,text,uuid,uuid,text,text,text,text,text,integer,bytea,text,jsonb,smallint,integer,timestamp with time zone,bytea) SET search_path TO 'pg_catalog', 'public', 'pg_temp';
ALTER FUNCTION public.replay_job_message(uuid,text,timestamp with time zone,bytea) SET search_path TO 'pg_catalog', 'public', 'pg_temp';
RESET ROLE;
