-- Reverting removes the table-level refusal of a duplicate GitHub source; the
-- host's own check in the connect transaction is unaffected by this schema.
DROP INDEX IF EXISTS public.datasource_sources_github_target_key;

ALTER TABLE public.datasource_sources DROP COLUMN IF EXISTS github_target_key;
