DROP POLICY IF EXISTS actor_chain_revocations_tenant ON actor_chain_revocations;
DROP POLICY IF EXISTS actor_chain_journal_tenant ON actor_chain_journal;

DROP TABLE IF EXISTS actor_chain_revocations;
DROP TABLE IF EXISTS actor_chain_journal;

DROP FUNCTION IF EXISTS public.actor_chain_revocation_bump_authorization();
DROP FUNCTION IF EXISTS actor_chain_immutable();
