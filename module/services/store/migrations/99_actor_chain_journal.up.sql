-- Durable, linked, revocable on-behalf-of actor chain (RFC-0003).
--
-- The capability chain already enforces attenuation correctly inside the signed,
-- short-lived Work Context token. What the token cannot do is outlive itself: once
-- it expires there is no record of who acted for whom (B14), the per-hop
-- delegation_id is an ephemeral UUID linked to nothing, and there is no way to
-- revoke a single hop. These two append-only tables close that gap.
--
--   * actor_chain_journal   — one row per on-behalf-of hop, content-addressed and
--                             hash-chained to its parent hop (UCAN-style). The
--                             row id IS the token's per-hop delegation_id, so the
--                             ephemeral token and the durable record share one
--                             identifier; delegation_grant_id links a hop to the
--                             human-approved grant that authorized it, when one
--                             exists.
--   * actor_chain_revocations — the per-hop revocation list (Biscuit-style).
--                             Revoking a hop's revocation_id kills the hop and
--                             every descendant that chains through it. This layers
--                             on the coarse authorization_revision epoch already
--                             sealed into the token; the token's short TTL bounds
--                             the worst-case window either way.
--
-- Both tables are append-only: an immutable trigger rejects UPDATE/DELETE, so the
-- journal is tamper-evident in depth (the hash chain) and in breadth (no edits).

CREATE TABLE actor_chain_journal (
    id                     UUID PRIMARY KEY,
    org_id                 UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    task_id                TEXT,
    session_id             TEXT,
    owner_principal_id     UUID NOT NULL,
    actor_principal_id     UUID NOT NULL,
    actor_kind             TEXT NOT NULL,
    -- The hop this one narrows from. A soft reference (not a foreign key) so a
    -- child issued from a token minted before this journal existed still records
    -- its own hop instead of failing closed; the ancestry walk simply stops at a
    -- missing parent.
    parent_delegation_id   UUID,
    -- Linkage to the human-approved escalation grant that authorized this hop,
    -- when the hop came from one. NULL for hops issued from the owner's own
    -- standing authority.
    delegation_grant_id    UUID REFERENCES delegation_grants(id),
    granted_scopes         JSONB NOT NULL DEFAULT '[]'::jsonb,
    authorization_revision BIGINT NOT NULL CHECK (authorization_revision > 0),
    -- Per-hop revocation handle. Revoking this value (see actor_chain_revocations)
    -- kills this hop and everything downstream of it.
    revocation_id          UUID NOT NULL UNIQUE,
    hop_index              INT NOT NULL CHECK (hop_index >= 0),
    -- Content address of the parent hop, folded into this hop's own address.
    prev_hash              TEXT,
    hop_hash               TEXT NOT NULL,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Reconstruct a full chain, and walk ancestry from a given hop during the
-- revocation check.
CREATE INDEX actor_chain_journal_org_task_idx
    ON actor_chain_journal (org_id, task_id, session_id, hop_index);
CREATE INDEX actor_chain_journal_parent_idx
    ON actor_chain_journal (parent_delegation_id)
    WHERE parent_delegation_id IS NOT NULL;
CREATE INDEX actor_chain_journal_actor_idx
    ON actor_chain_journal (org_id, actor_principal_id, created_at DESC);

CREATE TABLE actor_chain_revocations (
    revocation_id          UUID PRIMARY KEY,
    org_id                 UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    revoked_by_principal_id UUID,
    reason                 TEXT,
    revoked_at             TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX actor_chain_revocations_org_idx
    ON actor_chain_revocations (org_id, revoked_at DESC);

-- Append-only enforcement, shared by both tables.
CREATE OR REPLACE FUNCTION actor_chain_immutable() RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'actor chain journal is append-only';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER actor_chain_journal_no_update
    BEFORE UPDATE ON actor_chain_journal FOR EACH ROW
    EXECUTE FUNCTION actor_chain_immutable();
CREATE TRIGGER actor_chain_journal_no_delete
    BEFORE DELETE ON actor_chain_journal FOR EACH ROW
    EXECUTE FUNCTION actor_chain_immutable();
CREATE TRIGGER actor_chain_revocations_no_update
    BEFORE UPDATE ON actor_chain_revocations FOR EACH ROW
    EXECUTE FUNCTION actor_chain_immutable();
CREATE TRIGGER actor_chain_revocations_no_delete
    BEFORE DELETE ON actor_chain_revocations FOR EACH ROW
    EXECUTE FUNCTION actor_chain_immutable();

-- Tenant RLS. Both tables are org-scoped. The FOR ALL policy keeps the
-- trigger-guarded UPDATE/DELETE verbs admitted so the rls-migration-gate stays
-- satisfied even though no role is granted those verbs; the control plane reaches
-- rows under its BYPASSRLS role.
ALTER TABLE actor_chain_journal ENABLE ROW LEVEL SECURITY;
ALTER TABLE actor_chain_journal FORCE  ROW LEVEL SECURITY;
CREATE POLICY actor_chain_journal_tenant ON actor_chain_journal
    USING (org_id::text = current_setting('app.current_org_id', true))
    WITH CHECK (org_id::text = current_setting('app.current_org_id', true));

ALTER TABLE actor_chain_revocations ENABLE ROW LEVEL SECURITY;
ALTER TABLE actor_chain_revocations FORCE  ROW LEVEL SECURITY;
CREATE POLICY actor_chain_revocations_tenant ON actor_chain_revocations
    USING (org_id::text = current_setting('app.current_org_id', true))
    WITH CHECK (org_id::text = current_setting('app.current_org_id', true));

-- Explicit per-relation grants (required from migration 61). Append-only, so no
-- role gets UPDATE/DELETE: the tenant path reads and appends.
GRANT SELECT, INSERT ON actor_chain_journal    TO app_tenant;
GRANT SELECT, INSERT ON actor_chain_journal    TO app_control_plane;
GRANT SELECT, INSERT ON actor_chain_revocations TO app_tenant;
GRANT SELECT, INSERT ON actor_chain_revocations TO app_control_plane;

-- Unified-audit linkage: record the on-behalf-of principal and the actor-chain
-- hop that authorized an audited action, so the audit trail joins to the durable
-- chain instead of losing the acting relationship at the DB boundary. Nullable —
-- most events are not on-behalf-of. actor_chain_hop_id correlates to
-- actor_chain_journal.id (a soft reference; audit_events is partitioned and
-- append-only reference data, so no cross-table foreign key is imposed).
ALTER TABLE audit_events
    ADD COLUMN on_behalf_of_principal_id UUID,
    ADD COLUMN actor_chain_hop_id        UUID;
