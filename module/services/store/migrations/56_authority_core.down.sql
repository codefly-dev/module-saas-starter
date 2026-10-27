-- Withdrawing the authority core's records ends every installation, grant and
-- delegation it decided, and every capability it issued with them.
-- The delegations the core holds return to accounts' own relation (migration
-- 51), as they stood.
-- Delegations to a module: the host authority interface's Delegate. A signed-in
-- person grants one composed module the right to act for them beyond any
-- session, on scopes they hold; the module then issues from the delegation
-- through a binding it declares (Issue with a delegation). Every issue re-reads
-- the row and what its grantor holds at that moment, so a delegation never
-- confers more than its grantor still holds.
--
-- The grant is immutable once recorded. A row is never deleted by the host:
-- revocation stamps revoked_at and who revoked it (the grantor, or an
-- administrator of the organisation), so what was delegated, to which module
-- and why stays readable after it stopped. Every capability issued under a
-- delegation is sealed to it, and the seal reads this row live: revocation
-- takes effect immediately, for this delegation's capabilities alone.

CREATE TABLE public.module_delegations (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    org_id uuid NOT NULL,
    principal_id uuid NOT NULL,
    module_prefix text NOT NULL,
    scopes jsonb NOT NULL,
    purpose text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    revoked_at timestamp with time zone,
    revoked_by uuid,
    CONSTRAINT module_delegations_module_prefix_check CHECK ((module_prefix ~ '^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$'::text) AND (length(module_prefix) <= 63)),
    CONSTRAINT module_delegations_scopes_check CHECK ((jsonb_typeof(scopes) = 'array'::text) AND (jsonb_array_length(scopes) > 0)),
    CONSTRAINT module_delegations_purpose_check CHECK (length(purpose) <= 1024),
    CONSTRAINT module_delegations_revocation_whole CHECK (((revoked_at IS NULL) = (revoked_by IS NULL)))
);

ALTER TABLE ONLY public.module_delegations FORCE ROW LEVEL SECURITY;

ALTER TABLE ONLY public.module_delegations
    ADD CONSTRAINT module_delegations_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.module_delegations
    ADD CONSTRAINT module_delegations_org_id_fkey FOREIGN KEY (org_id) REFERENCES public.organizations(id) ON DELETE CASCADE;

CREATE INDEX module_delegations_grantor ON public.module_delegations USING btree (org_id, principal_id, created_at DESC);

ALTER TABLE public.module_delegations ENABLE ROW LEVEL SECURITY;

CREATE POLICY module_delegations_tenant ON public.module_delegations
    USING ((org_id = (NULLIF(current_setting('app.current_org_id'::text, true), ''::text))::uuid))
    WITH CHECK ((org_id = (NULLIF(current_setting('app.current_org_id'::text, true), ''::text))::uuid));

CREATE POLICY app_control_plane_explicit_rows ON public.module_delegations
    TO app_control_plane
    USING ((CURRENT_USER = 'app_control_plane'::name))
    WITH CHECK ((CURRENT_USER = 'app_control_plane'::name));

-- No DELETE for either role: a delegation is revoked, never erased (the
-- organization's own deletion removes its rows through the foreign key).
GRANT SELECT,INSERT,UPDATE ON TABLE public.module_delegations TO app_tenant;
GRANT SELECT,INSERT,UPDATE ON TABLE public.module_delegations TO app_control_plane;

-- Only the revocation stamp may change, and only once.
CREATE FUNCTION public.module_delegation_immutable() RETURNS trigger
LANGUAGE plpgsql SECURITY INVOKER SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
    IF (NEW.id, NEW.org_id, NEW.principal_id, NEW.module_prefix, NEW.scopes, NEW.purpose, NEW.created_at)
       IS DISTINCT FROM (OLD.id, OLD.org_id, OLD.principal_id, OLD.module_prefix, OLD.scopes, OLD.purpose, OLD.created_at)
       OR (OLD.revoked_at IS NOT NULL AND (NEW.revoked_at, NEW.revoked_by) IS DISTINCT FROM (OLD.revoked_at, OLD.revoked_by)) THEN
        RAISE EXCEPTION 'a module delegation is immutable once recorded and is revoked at most once' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER module_delegation_immutable BEFORE UPDATE ON public.module_delegations
    FOR EACH ROW EXECUTE FUNCTION public.module_delegation_immutable();

REVOKE ALL ON FUNCTION public.module_delegation_immutable() FROM PUBLIC;

INSERT INTO public.module_delegations (id, org_id, principal_id, module_prefix, scopes, purpose, revoked_at, revoked_by)
SELECT d.id::uuid, d.organization_id, d.grantor::uuid, d.module,
       COALESCE((SELECT jsonb_agg(jsonb_strip_nulls(jsonb_build_object(
                    'resource_kind', s->>'kind', 'actions', s->'actions', 'resource_ids', s->'ids')))
                   FROM jsonb_array_elements(d.scopes) AS s), '[]'::jsonb),
       d.purpose,
       CASE WHEN d.revoked THEN CURRENT_TIMESTAMP END,
       CASE WHEN d.revoked THEN d.grantor::uuid END
  FROM public.authority_core_delegations d
 WHERE d.grantor ~ '^[0-9a-f-]{36}$' AND d.id ~ '^[0-9a-f-]{36}$' AND d.module ~ '^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$';

DROP TABLE public.authority_core_replay;
DROP TABLE public.authority_core_owner_aware;
DROP TABLE public.authority_core_grant_epochs;
DROP TABLE public.authority_core_person_epochs;
DROP TABLE public.authority_core_delegations;
DROP TABLE public.authority_core_role_grants;
DROP TABLE public.authority_core_module_revisions;
DROP TABLE public.authority_core_installations;
DROP TABLE public.authority_core_tombstones;
DROP TABLE public.authority_core_modules;
