DROP FUNCTION public.lock_installation_approval_target(uuid);
ALTER TABLE public.installations DROP COLUMN consent_receipt;
