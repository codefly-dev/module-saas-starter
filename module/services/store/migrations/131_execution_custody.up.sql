-- Private tenant-owned secret custody; only the Accounts control plane can
-- access it. Workers receive children from the authenticated broker, never SQL
-- or Vault credentials. Tenant RLS is defense in depth for future grants.
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
-- Deny-all request policy: even an accidental future tenant grant cannot expose
-- ciphertext. Only the existing Accounts control plane may mediate access.
CREATE POLICY execution_custody_private ON public.execution_custody FOR ALL
 TO app_tenant USING (false) WITH CHECK (false);
REVOKE ALL ON public.execution_custody FROM PUBLIC, app_tenant;
GRANT SELECT, INSERT, DELETE ON public.execution_custody TO app_control_plane;
GRANT UPDATE (envelope) ON public.execution_custody TO app_control_plane;
CREATE INDEX execution_custody_expiry ON public.execution_custody (expires_at);
