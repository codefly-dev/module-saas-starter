-- The last successful approval is a retry receipt, scoped by the installation's
-- existing tenant RLS. A later lifecycle revision invalidates it.
ALTER TABLE public.installations ADD COLUMN consent_receipt jsonb NOT NULL DEFAULT '{}'::jsonb;

-- Tenant traffic cannot read the presence tables. This narrow definer shares
-- the delivery writer's binding/target locks until the consent write commits.
CREATE FUNCTION public.lock_installation_approval_target(target uuid) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $$
DECLARE
    binding text;
    generation bigint;
    removed boolean;
    closed bigint;
BEGIN
    SELECT binding_id INTO binding FROM public.solution_targets WHERE id=target;
    SELECT applied_generation, applied_removed INTO generation, removed
      FROM public.solution_host_bindings WHERE binding_id=binding FOR SHARE;
    SELECT closed_generation INTO closed FROM public.solution_targets WHERE id=target FOR SHARE;
    IF generation IS NULL OR removed OR closed IS NOT NULL THEN
        RETURN 0;
    END IF;
    RETURN generation;
END;
$$;
REVOKE ALL ON FUNCTION public.lock_installation_approval_target(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.lock_installation_approval_target(uuid) TO app_tenant, app_control_plane;
GRANT CREATE ON SCHEMA public TO app_control_plane;
ALTER FUNCTION public.lock_installation_approval_target(uuid) OWNER TO app_control_plane;
REVOKE CREATE ON SCHEMA public FROM app_control_plane;
