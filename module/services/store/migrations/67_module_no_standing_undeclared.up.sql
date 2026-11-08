-- A paired module declaration that selects no installation role and declares
-- no role at all leaves the host nothing to select: the business layer installs
-- such a module holding no standing authority, as it installs one declaring
-- `none` (accounts, business.supportingInstallationRole), because all a
-- solution's closure asks of a supporting module is that it be installed.
-- Migration 64's check of the current paired declaration knew only the literal
-- `none`, so the install the review accepted was refused one step later. This
-- teaches it the same rule, under the same delivery locks. `roles` is encoded
-- only when the declaration names at least one role, so its absence is exactly
-- "declares no role"; a declaration that names roles but selects none of them
-- still does not select no standing authority.
--
-- The function is owned by app_control_plane, so it is replaced as that role,
-- which is the idiom migration 64 established for exactly this.
GRANT CREATE ON SCHEMA public TO app_control_plane;
SET LOCAL ROLE app_control_plane;
CREATE OR REPLACE FUNCTION public.lock_module_no_standing_declaration(target uuid) RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $$
DECLARE selected boolean;
BEGIN
    PERFORM public.lock_installation_approval_target(target);
    SELECT (m.declarations->>'installation_role'='none')
        OR (m.declarations->>'installation_role' IS NULL AND m.declarations->'roles' IS NULL)
      INTO selected
      FROM public.solution_targets t JOIN public.solution_host_bindings b ON b.binding_id=t.binding_id
      JOIN public.authority_core_modules m ON m.name=t.solution_id
      WHERE t.id=target AND t.closed_generation IS NULL AND NOT b.applied_removed
        AND m.kind='module' AND m.generation=b.applied_generation FOR SHARE OF m;
    RETURN COALESCE(selected,false);
END;
$$;
RESET ROLE;
REVOKE CREATE ON SCHEMA public FROM app_control_plane;
