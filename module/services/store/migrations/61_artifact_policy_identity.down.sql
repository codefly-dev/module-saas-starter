-- Refuse rollback if independent policies now hold the same content: never
-- discard a consent receipt to make an older schema fit.
ALTER TABLE public.executable_artifact_approvals
 DROP CONSTRAINT executable_artifact_policy_identity,
 ADD UNIQUE (org_id, installation_id, module_principal_id, subject_digest);
