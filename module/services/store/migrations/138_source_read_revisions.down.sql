DROP INDEX IF EXISTS public.record_shares_source_read_expiry;
DROP INDEX IF EXISTS public.scope_grants_source_read_expiry;
DROP INDEX IF EXISTS public.datasource_sources_org_source_read;
DROP TRIGGER IF EXISTS record_shares_source_read_revision ON public.record_shares;
DROP TRIGGER IF EXISTS scope_grants_source_read_revision ON public.scope_grants;
DROP TRIGGER IF EXISTS scope_nodes_source_read_revision ON public.scope_nodes;
DROP TRIGGER IF EXISTS datasource_sources_source_read_revision ON public.datasource_sources;
DROP FUNCTION IF EXISTS public.bump_source_read_revision();
DROP TABLE IF EXISTS public.source_read_revisions;
