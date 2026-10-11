-- Downgrade refuses while new-kind receipts exist; never erase policy evidence.
ALTER TABLE public.policy_log_commits DROP CONSTRAINT policy_log_commits_subject_kind_check;
ALTER TABLE public.policy_log_commits ADD CONSTRAINT policy_log_commits_subject_kind_check
    CHECK (subject_kind IN ('principal','binding','installation','team_membership','scope_grant','organization'));
DROP TRIGGER installation_registry_on_insert ON public.installations;
DROP FUNCTION public.installation_registry_on_insert();
DROP FUNCTION public.check_installation_envelope(bigint);
DROP FUNCTION public.lock_module_job_authority(jsonb);
DROP FUNCTION public.module_job_authority_current(jsonb);
DROP TRIGGER cancel_installation_work ON public.installations;
DROP FUNCTION public.cancel_installation_work();
ALTER TABLE public.job_messages DROP COLUMN consumer_authority;
DROP TRIGGER installation_authority_revision ON public.installations;
DROP FUNCTION public.installation_authority_revision();
DROP INDEX public.installations_active_principal;
ALTER TABLE public.installations DROP CONSTRAINT installations_registry_target_fkey,
    DROP CONSTRAINT installations_active_registry,
    DROP COLUMN kind, DROP COLUMN registry_binding_id, DROP COLUMN registry_principal_id,
    DROP COLUMN scope, DROP COLUMN revision;
ALTER TABLE public.solution_targets DROP CONSTRAINT solution_targets_id_binding_unique;

CREATE OR REPLACE FUNCTION public.job_state_transition_is_valid(from_state text, to_state text) RETURNS boolean
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE SET search_path = pg_catalog AS $$
 SELECT CASE from_state
 WHEN 'pending' THEN to_state IN ('processing','canceled')
 WHEN 'processing' THEN to_state IN ('retrying','succeeded','dead_letter')
 WHEN 'retrying' THEN to_state IN ('processing','canceled') ELSE FALSE END;
$$;
