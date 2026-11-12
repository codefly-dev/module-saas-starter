-- The consent receipt the authority core records on a module page's own
-- installation record: composition's receipt, which holds the installation at
-- the reach the page had when it was taken, or an approved upgrade's. The
-- record is accounts' own, so what the core records on it is kept here,
-- beside the revision it raises and what it owns; a row may now hold a
-- receipt with no raise and nothing owned.
SET LOCAL lock_timeout = '15s';
ALTER TABLE public.authority_core_module_revisions ADD COLUMN consent jsonb DEFAULT '{}'::jsonb NOT NULL;
ALTER TABLE public.authority_core_module_revisions ADD CONSTRAINT authority_core_module_revisions_consent_check CHECK ((jsonb_typeof(consent) = 'object'));
