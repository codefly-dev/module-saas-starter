-- What a module's own installation record owns: the resources the authority
-- core attached to a composition installation (a module page's, whose roles
-- reach a module that checks owners as the data its installation owns). The
-- record is accounts' own, so what the core records on it is kept here, beside
-- the revision it raises; a row may now hold an attachment with no raise.
SET LOCAL lock_timeout = '15s';
ALTER TABLE public.authority_core_module_revisions ADD COLUMN owned jsonb DEFAULT '{}'::jsonb NOT NULL;
ALTER TABLE public.authority_core_module_revisions DROP CONSTRAINT authority_core_module_revisions_raised_check;
ALTER TABLE public.authority_core_module_revisions ADD CONSTRAINT authority_core_module_revisions_raised_check CHECK ((raised >= 0));
ALTER TABLE public.authority_core_module_revisions ADD CONSTRAINT authority_core_module_revisions_owned_check CHECK ((jsonb_typeof(owned) = 'object'));
