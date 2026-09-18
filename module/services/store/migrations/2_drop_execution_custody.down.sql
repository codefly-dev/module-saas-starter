-- Restores the relation exactly as migrations 131 and 136 left it.
CREATE TABLE public.execution_custody (
 reference UUID PRIMARY KEY,
 org_id UUID NOT NULL REFERENCES public.organizations(id) ON DELETE CASCADE,
 owner_id UUID NOT NULL REFERENCES public.users(uuid) ON DELETE CASCADE,
 admission_id TEXT NOT NULL CHECK (length(admission_id) BETWEEN 1 AND 128),
 fingerprint TEXT NOT NULL CHECK (length(fingerprint) = 64),
 envelope TEXT NOT NULL CHECK (envelope = '' OR envelope LIKE 'cfs1:vault-transit:%'),
 expires_at TIMESTAMPTZ NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
 UNIQUE (org_id, owner_id, admission_id)
);
ALTER TABLE public.execution_custody ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.execution_custody FORCE ROW LEVEL SECURITY;
CREATE POLICY execution_custody_private ON public.execution_custody FOR ALL
 TO app_tenant USING (false) WITH CHECK (false);
CREATE POLICY app_control_plane_explicit_rows ON public.execution_custody FOR ALL TO app_control_plane
 USING (current_user = 'app_control_plane') WITH CHECK (current_user = 'app_control_plane');
REVOKE ALL ON public.execution_custody FROM PUBLIC, app_tenant;
GRANT SELECT, INSERT, DELETE ON public.execution_custody TO app_control_plane;
GRANT UPDATE (envelope) ON public.execution_custody TO app_control_plane;
CREATE INDEX execution_custody_expiry ON public.execution_custody (expires_at);
