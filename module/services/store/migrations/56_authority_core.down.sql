-- Withdrawing the authority core's records ends every installation, grant and
-- delegation it decided, and every capability it issued with them.
DROP TABLE public.authority_core_replay;
DROP TABLE public.authority_core_owner_aware;
DROP TABLE public.authority_core_grant_epochs;
DROP TABLE public.authority_core_person_epochs;
DROP TABLE public.authority_core_delegations;
DROP TABLE public.authority_core_role_grants;
DROP TABLE public.authority_core_installations;
DROP TABLE public.authority_core_tombstones;
DROP TABLE public.authority_core_modules;
