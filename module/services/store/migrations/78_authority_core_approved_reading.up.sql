-- An approved declaration is kept per module, generation and offer digest.
-- The authority core reads a generation it holds again when its host learns
-- a declaration field (authority.Service.RereadModule): the same delivery,
-- read as another declaration at the same generation. A consent taken before
-- the re-read approved the earlier reading and one taken after approves the
-- new one; both must stay readable, each by the digest its consent recorded.
-- A row kept before this carries no digest (''): the store reads it by the
-- digest it computes from the row, as the core always compared it.
SET LOCAL lock_timeout = '15s';
ALTER TABLE public.authority_core_approved_modules ADD COLUMN offer_digest text DEFAULT '' NOT NULL;
ALTER TABLE public.authority_core_approved_modules DROP CONSTRAINT authority_core_approved_modules_pkey;
ALTER TABLE public.authority_core_approved_modules ADD CONSTRAINT authority_core_approved_modules_pkey PRIMARY KEY (name, generation, offer_digest);
