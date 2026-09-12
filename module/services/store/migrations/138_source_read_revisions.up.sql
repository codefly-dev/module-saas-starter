-- Source cursors must notice changes without rereading every source and grant.
CREATE TABLE public.source_read_revisions (
 org_id UUID PRIMARY KEY REFERENCES public.organizations(id) ON DELETE CASCADE,
 revision BIGINT NOT NULL DEFAULT 1
);
ALTER TABLE public.source_read_revisions ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.source_read_revisions FORCE ROW LEVEL SECURITY;
CREATE POLICY source_read_revisions_tenant ON public.source_read_revisions
 USING (org_id = NULLIF(current_setting('app.current_org_id', true), '')::uuid);
GRANT SELECT ON public.source_read_revisions TO app_tenant;
GRANT SELECT, INSERT, UPDATE ON public.source_read_revisions TO app_control_plane;
CREATE POLICY app_control_plane_explicit_rows ON public.source_read_revisions
 FOR ALL TO app_control_plane
 USING (current_user = 'app_control_plane') WITH CHECK (current_user = 'app_control_plane');

CREATE FUNCTION public.bump_source_read_revision() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public AS $$
DECLARE target UUID;
BEGIN
 FOR target IN SELECT DISTINCT id FROM unnest(ARRAY[
 CASE WHEN TG_OP <> 'INSERT' THEN OLD.org_id END,
 CASE WHEN TG_OP <> 'DELETE' THEN NEW.org_id END]) AS changed(id) WHERE id IS NOT NULL
 LOOP
  -- Parent deletion cascades must not resurrect the revision row.
  IF EXISTS (SELECT 1 FROM public.organizations WHERE id=target) THEN
   INSERT INTO public.source_read_revisions(org_id) VALUES(target)
   ON CONFLICT(org_id) DO UPDATE SET revision=source_read_revisions.revision+1;
  END IF;
 END LOOP;
 RETURN NULL;
END;
$$;
CREATE TRIGGER datasource_sources_source_read_revision
 AFTER INSERT OR DELETE OR UPDATE OF org_id, provider, repo, branch, paths, boundary_node_id ON public.datasource_sources
 FOR EACH ROW EXECUTE FUNCTION public.bump_source_read_revision();
CREATE TRIGGER scope_nodes_source_read_revision AFTER INSERT OR UPDATE OR DELETE ON public.scope_nodes
 FOR EACH ROW EXECUTE FUNCTION public.bump_source_read_revision();
CREATE TRIGGER scope_grants_source_read_revision AFTER INSERT OR UPDATE OR DELETE ON public.scope_grants
 FOR EACH ROW EXECUTE FUNCTION public.bump_source_read_revision();
CREATE TRIGGER record_shares_source_read_revision AFTER INSERT OR UPDATE OR DELETE ON public.record_shares
 FOR EACH ROW EXECUTE FUNCTION public.bump_source_read_revision();
-- Managed PostgreSQL has no bypass role: give the trigger exactly the
-- control-plane policy and SQL privileges needed to advance a revision.
REVOKE ALL ON FUNCTION public.bump_source_read_revision() FROM PUBLIC;
GRANT CREATE ON SCHEMA public TO app_control_plane;
ALTER FUNCTION public.bump_source_read_revision() OWNER TO app_control_plane;
REVOKE CREATE ON SCHEMA public FROM app_control_plane;
CREATE INDEX datasource_sources_org_source_read ON public.datasource_sources(org_id,id);
CREATE INDEX scope_grants_source_read_expiry ON public.scope_grants(org_id,expires_at) WHERE expires_at IS NOT NULL;
CREATE INDEX record_shares_source_read_expiry ON public.record_shares(org_id,expires_at) WHERE expires_at IS NOT NULL;
