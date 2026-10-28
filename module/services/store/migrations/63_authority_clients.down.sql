-- This discards client identities and client-grantee metadata. It is not a
-- behavior-preserving rollback after client delegation has been used.
ALTER TABLE public.authority_core_delegations
 DROP COLUMN client_id,
 DROP COLUMN audience,
 DROP COLUMN resource;
DROP TABLE public.authority_core_clients;
