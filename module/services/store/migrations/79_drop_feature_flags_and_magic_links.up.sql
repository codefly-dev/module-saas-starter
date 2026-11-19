-- The database-backed flag inventory and magic-link sign-in are gone from the
-- host. Nothing evaluated a flag and no runtime role could write one; no route
-- ever minted or redeemed a magic link. Accounts no longer reads either
-- relation, so both are dropped with their policies, indexes and grants.
--
-- The 'magic_link' rows of identity_providers and email_templates stay: an
-- identity recorded under that provider keeps its foreign key.
SET LOCAL lock_timeout = '15s';
DROP TABLE public.feature_flags;
DROP TABLE public.magic_links;
