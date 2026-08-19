ALTER TABLE audit_events
    DROP COLUMN IF EXISTS actor_chain_hop_id,
    DROP COLUMN IF EXISTS on_behalf_of_principal_id;

DROP POLICY IF EXISTS actor_chain_revocations_tenant ON actor_chain_revocations;
DROP POLICY IF EXISTS actor_chain_journal_tenant ON actor_chain_journal;

DROP TABLE IF EXISTS actor_chain_revocations;
DROP TABLE IF EXISTS actor_chain_journal;

DROP FUNCTION IF EXISTS actor_chain_immutable();
