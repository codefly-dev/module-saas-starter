-- The listings an object-storage source was synced at. An object store answers
-- only "what is there now", so the host keeps the listing each version names:
-- the next sync diffs the current listing against it, and a module fetches an
-- object at the version it was told of. The version is the listing's own
-- digest under the source's binding, so a row is written once and never
-- changes; only the newest few per source are kept. Tenant-owned: the control
-- plane neither reads nor writes it.
CREATE TABLE public.datasource_object_manifests (
 org_id uuid NOT NULL,
 source_id uuid NOT NULL,
 version text NOT NULL CHECK (version ~ '^[a-f0-9]{16}\.[a-f0-9]{64}$'),
 objects jsonb NOT NULL CHECK (jsonb_typeof(objects) = 'array'),
 retained_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY (org_id,source_id) REFERENCES public.datasource_sources(org_id,id) ON DELETE CASCADE,
 PRIMARY KEY (org_id,source_id,version)
);
CREATE INDEX datasource_object_manifests_retained ON public.datasource_object_manifests (org_id,source_id,retained_at DESC);
ALTER TABLE public.datasource_object_manifests ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.datasource_object_manifests FORCE ROW LEVEL SECURITY;
CREATE POLICY datasource_object_manifests_tenant ON public.datasource_object_manifests TO app_tenant
 USING (org_id = NULLIF(current_setting('app.current_org_id', true), '')::uuid)
 WITH CHECK (org_id = NULLIF(current_setting('app.current_org_id', true), '')::uuid);
GRANT SELECT, INSERT, UPDATE, DELETE ON public.datasource_object_manifests TO app_tenant;
