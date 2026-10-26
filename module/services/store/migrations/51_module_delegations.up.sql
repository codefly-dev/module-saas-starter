-- Delegations to a module: the host authority interface's Delegate. A signed-in
-- person grants one composed module the right to act for them beyond any
-- session, on scopes they hold; the module then issues from the delegation
-- through a binding it declares (Issue with a delegation). Every issue re-reads
-- the row and what its grantor holds at that moment, so a delegation never
-- confers more than its grantor still holds.
--
-- The grant is immutable once recorded. A row is never deleted by the host:
-- revocation stamps revoked_at and who revoked it, so what was delegated, to
-- which module and why stays readable after it stopped. Revoking advances the
-- grantor's authorization revision in the same statement, so every capability
-- already issued from the delegation fails its next verification: revocation
-- takes effect immediately, not at the capability's expiry.

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

-- Only the revocation stamp may change, and only once; the revocation advances
-- the grantor's authorization revision, which every capability they own is
-- sealed to.
CREATE FUNCTION public.module_delegation_revocation() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
    IF (NEW.id, NEW.org_id, NEW.principal_id, NEW.module_prefix, NEW.scopes, NEW.purpose, NEW.created_at)
       IS DISTINCT FROM (OLD.id, OLD.org_id, OLD.principal_id, OLD.module_prefix, OLD.scopes, OLD.purpose, OLD.created_at)
       OR (OLD.revoked_at IS NOT NULL AND (NEW.revoked_at, NEW.revoked_by) IS DISTINCT FROM (OLD.revoked_at, OLD.revoked_by)) THEN
        RAISE EXCEPTION 'a module delegation is immutable once recorded and is revoked at most once' USING ERRCODE = 'check_violation';
    END IF;
    IF OLD.revoked_at IS NULL AND NEW.revoked_at IS NOT NULL THEN
        PERFORM public.bump_principal_authorization_revision(NEW.org_id, NEW.principal_id);
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER module_delegation_revocation BEFORE UPDATE ON public.module_delegations
    FOR EACH ROW EXECUTE FUNCTION public.module_delegation_revocation();

REVOKE ALL ON FUNCTION public.module_delegation_revocation() FROM PUBLIC;
GRANT CREATE ON SCHEMA public TO app_control_plane;
ALTER FUNCTION public.module_delegation_revocation() OWNER TO app_control_plane;
REVOKE CREATE ON SCHEMA public FROM app_control_plane;
