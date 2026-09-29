-- Reverts migration 12: restores each function's baseline search_path without
-- pg_temp, which reopens the shadowing: a caller's temporary relations are again
-- searched before the schemas the function names. The owning roles are assumed
-- for the same reason as in the up migration.

-- Owned by the migration principal.
ALTER FUNCTION public.append_job_state_transition() SET search_path TO 'pg_catalog', 'public';
ALTER FUNCTION public.audit_events_drop_partitions_before(timestamp with time zone) SET search_path TO 'public';
ALTER FUNCTION public.record_delivery_event(text,text,text,text,text,timestamp with time zone,uuid) SET search_path TO 'pg_catalog', 'public';
ALTER FUNCTION public.sync_webhook_event_subscriptions(uuid,uuid,text[]) SET search_path TO 'pg_catalog', 'public';

-- Owned by app_control_plane.
SET LOCAL ROLE app_control_plane;
ALTER FUNCTION public.actor_chain_revocation_bump_authorization() SET search_path TO 'pg_catalog', 'public';
ALTER FUNCTION public.authorization_revision_membership_mutation() SET search_path TO 'pg_catalog', 'public';
ALTER FUNCTION public.authorization_revision_organization_insert() SET search_path TO 'pg_catalog', 'public';
ALTER FUNCTION public.authorization_revision_platform_admin_mutation() SET search_path TO 'pg_catalog', 'public';
ALTER FUNCTION public.authorization_revision_principal_mutation() SET search_path TO 'pg_catalog', 'public';
ALTER FUNCTION public.authorization_revision_role_assignment_mutation() SET search_path TO 'pg_catalog', 'public';
ALTER FUNCTION public.authorization_revision_role_mutation() SET search_path TO 'pg_catalog', 'public';
ALTER FUNCTION public.authorization_revision_role_permission_mutation() SET search_path TO 'pg_catalog', 'public';
ALTER FUNCTION public.authorization_revision_team_member_mutation() SET search_path TO 'pg_catalog', 'public';
ALTER FUNCTION public.authorization_revision_team_mutation() SET search_path TO 'pg_catalog', 'public';
ALTER FUNCTION public.authorization_revision_user_mutation() SET search_path TO 'pg_catalog', 'public';
ALTER FUNCTION public.bump_organization_authorization_revision(uuid) SET search_path TO 'pg_catalog', 'public';
ALTER FUNCTION public.bump_principal_and_organization_authorization(uuid,uuid) SET search_path TO 'pg_catalog', 'public';
ALTER FUNCTION public.bump_principal_authorization_revision(uuid,uuid) SET search_path TO 'pg_catalog', 'public';
ALTER FUNCTION public.bump_role_authorization_revision(uuid,uuid) SET search_path TO 'pg_catalog', 'public';
ALTER FUNCTION public.bump_source_read_revision() SET search_path TO 'pg_catalog', 'public';
ALTER FUNCTION public.identity_administered_organizations(uuid) SET search_path TO 'pg_catalog', 'public';
ALTER FUNCTION public.invalidate_authorization_sessions() SET search_path TO 'pg_catalog', 'public';
ALTER FUNCTION public.invalidate_scoped_role_sessions() SET search_path TO 'pg_catalog', 'public';
ALTER FUNCTION public.organization_eligible_administrators(uuid) SET search_path TO 'pg_catalog', 'public';
ALTER FUNCTION public.organization_member_primary_email(uuid) SET search_path TO 'pg_catalog', 'public';
ALTER FUNCTION public.publish_domain_event(uuid,text,text,text,timestamp with time zone,text,text,text,bytea,uuid,text,text,text,text,text,text,text,integer,bytea) SET search_path TO 'pg_catalog', 'public';
ALTER FUNCTION public.sync_human_principal() SET search_path TO 'pg_catalog', 'public';
RESET ROLE;

-- Owned by app_job_worker.
SET LOCAL ROLE app_job_worker;
ALTER FUNCTION public.enqueue_job_message(text,text,uuid,uuid,text,text,text,text,text,integer,bytea,text,jsonb,smallint,integer,timestamp with time zone,bytea) SET search_path TO 'pg_catalog', 'public';
ALTER FUNCTION public.replay_job_message(uuid,text,timestamp with time zone,bytea) SET search_path TO 'pg_catalog', 'public';
RESET ROLE;
