-- Keeps one declaration per (module, generation) again: the one kept first
-- (no digest), else the lowest digest. A consent that approved another
-- reading of that generation then reads it as not kept and is refused by
-- name until an administrator approves the delivered offer, failing closed.
SET LOCAL lock_timeout = '15s';
DELETE FROM public.authority_core_approved_modules a
 USING public.authority_core_approved_modules b
 WHERE a.name = b.name AND a.generation = b.generation AND a.offer_digest > b.offer_digest;
ALTER TABLE public.authority_core_approved_modules DROP CONSTRAINT authority_core_approved_modules_pkey;
ALTER TABLE public.authority_core_approved_modules DROP COLUMN offer_digest;
ALTER TABLE public.authority_core_approved_modules ADD CONSTRAINT authority_core_approved_modules_pkey PRIMARY KEY (name, generation);
