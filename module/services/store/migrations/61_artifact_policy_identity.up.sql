-- The shared artifact digest identifies content; policy is a separate part of
-- consent. Preserve existing receipts while allowing independent policy consent.
DO $$
DECLARE old_key name;
BEGIN
 SELECT conname INTO STRICT old_key FROM pg_catalog.pg_constraint
 WHERE conrelid = 'public.executable_artifact_approvals'::regclass AND contype = 'u';
 EXECUTE format('ALTER TABLE public.executable_artifact_approvals DROP CONSTRAINT %I', old_key);
END $$;
ALTER TABLE public.executable_artifact_approvals
 ADD CONSTRAINT executable_artifact_policy_identity
 UNIQUE (org_id, installation_id, module_principal_id, policy_id, subject_digest);
