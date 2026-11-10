-- Restores migration 64's body: only the literal `none` selects no standing
-- authority.
GRANT CREATE ON SCHEMA public TO app_control_plane;
SET LOCAL ROLE app_control_plane;
CREATE OR REPLACE FUNCTION public.lock_module_no_standing_declaration(target uuid) RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $$
DECLARE selected boolean;
BEGIN
    PERFORM public.lock_installation_approval_target(target);
    SELECT m.declarations->>'installation_role'='none' INTO selected
      FROM public.solution_targets t JOIN public.solution_host_bindings b ON b.binding_id=t.binding_id
      JOIN public.authority_core_modules m ON m.name=t.solution_id
      WHERE t.id=target AND t.closed_generation IS NULL AND NOT b.applied_removed
        AND m.kind='module' AND m.generation=b.applied_generation FOR SHARE OF m;
    RETURN COALESCE(selected,false);
END;
$$;
RESET ROLE;
REVOKE CREATE ON SCHEMA public FROM app_control_plane;
