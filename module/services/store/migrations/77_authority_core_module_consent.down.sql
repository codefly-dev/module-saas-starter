SET LOCAL lock_timeout = '15s';
ALTER TABLE public.authority_core_module_revisions DROP CONSTRAINT authority_core_module_revisions_consent_check;
ALTER TABLE public.authority_core_module_revisions DROP COLUMN consent;
DELETE FROM public.authority_core_module_revisions WHERE raised = 0 AND owned = '{}'::jsonb;
