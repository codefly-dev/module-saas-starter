SET LOCAL lock_timeout = '15s';
DELETE FROM public.authority_core_module_revisions WHERE raised = 0;
ALTER TABLE public.authority_core_module_revisions DROP CONSTRAINT authority_core_module_revisions_owned_check;
ALTER TABLE public.authority_core_module_revisions DROP CONSTRAINT authority_core_module_revisions_raised_check;
ALTER TABLE public.authority_core_module_revisions ADD CONSTRAINT authority_core_module_revisions_raised_check CHECK ((raised > 0));
ALTER TABLE public.authority_core_module_revisions DROP COLUMN owned;
