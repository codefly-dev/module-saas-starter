-- Immutable exact-content consent; neither executable bytes nor active pointers.
CREATE TABLE public.executable_artifact_approvals (
 id uuid PRIMARY KEY,
 org_id uuid NOT NULL REFERENCES public.organizations(id) ON DELETE CASCADE,
 installation_id uuid NOT NULL,
 module_principal_id uuid NOT NULL,
 policy_id text NOT NULL CHECK (length(policy_id) BETWEEN 1 AND 128),
 subject_digest text NOT NULL CHECK (subject_digest ~ '^sha256:[0-9a-f]{64}$'),
 subject bytea NOT NULL CHECK (octet_length(subject) BETWEEN 1 AND 524288),
 approved_by uuid NOT NULL,
 created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
 revoked_at timestamptz,
 revoked_by uuid,
 CHECK ((revoked_at IS NULL) = (revoked_by IS NULL)),
 UNIQUE (org_id, installation_id, module_principal_id, subject_digest)
);
ALTER TABLE public.executable_artifact_approvals ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.executable_artifact_approvals FORCE ROW LEVEL SECURITY;
CREATE POLICY executable_artifact_approvals_tenant ON public.executable_artifact_approvals
 TO app_tenant
 USING (org_id = NULLIF(current_setting('app.current_org_id', true), '')::uuid)
 WITH CHECK (org_id = NULLIF(current_setting('app.current_org_id', true), '')::uuid);
GRANT SELECT, INSERT ON public.executable_artifact_approvals TO app_tenant;
GRANT UPDATE (revoked_at, revoked_by) ON public.executable_artifact_approvals TO app_tenant;
-- A recorded revocation is terminal even for a holder of the application login.
CREATE FUNCTION public.executable_artifact_no_revival() RETURNS trigger LANGUAGE plpgsql
 SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
 IF OLD.revoked_at IS NOT NULL AND
    (NEW.revoked_at IS DISTINCT FROM OLD.revoked_at OR NEW.revoked_by IS DISTINCT FROM OLD.revoked_by) THEN
  RAISE EXCEPTION 'executable artifact revocation is terminal';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER executable_artifact_no_revival BEFORE UPDATE ON public.executable_artifact_approvals
 FOR EACH ROW EXECUTE FUNCTION public.executable_artifact_no_revival();
REVOKE ALL ON FUNCTION public.executable_artifact_no_revival() FROM PUBLIC;
