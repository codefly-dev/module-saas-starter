-- Replay store for SINGLE_USE Work Contexts (issue #420).
--
-- SINGLE_USE on a signed Work Context was only a label: nothing recorded that a
-- capability had been redeemed, so a single-use token could in fact be replayed.
-- This table is that record. Enforcement sits on the consumer/verifier side:
-- after a consumer cryptographically verifies a capability it claims the token's
-- own nonce (context_id) here, and the primary key admits exactly one claim per
-- id — the first wins, every replay fails closed.
--
-- A marker only needs to outlive its own capability. expires_at is the token's
-- sealed expiry; once the token is past it the verifier rejects it on time
-- grounds, so the marker is moot. A short sweep (PurgeExpiredWorkContextReplays,
-- run hourly — well under the 15-minute max token TTL) reclaims expired markers,
-- so the table holds roughly the live tokens plus at most one sweep interval of
-- expired ones, never a full retention day's worth.
--
-- Org-scoped and RLS-isolated like connector_credentials (migration 103):
-- visible/writable only when acting in the row's org; the control plane reaches
-- rows under its BYPASSRLS role (app_control_plane) for the cross-tenant GC.

CREATE TABLE IF NOT EXISTS work_context_replay (
    org_id      UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    context_id  TEXT NOT NULL,
    expires_at  TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (org_id, context_id)
);

-- Drives the retention sweep that reclaims markers of expired capabilities.
CREATE INDEX work_context_replay_expiry_idx ON work_context_replay (expires_at);

-- RLS: request-scoped own-org visibility only (direct-org_id recipe). The
-- FOR ALL policy admits every verb so the rls-migration-gate stays satisfied;
-- the explicit GRANTs below are what actually restrict each role's verbs. The
-- policy must never trust a client-settable GUC; the cross-tenant GC runs under
-- app_control_plane, whose BYPASSRLS is a database capability a caller cannot
-- manufacture with set_config().
ALTER TABLE work_context_replay ENABLE ROW LEVEL SECURITY;
ALTER TABLE work_context_replay FORCE  ROW LEVEL SECURITY;
CREATE POLICY work_context_replay_tenant ON work_context_replay
    USING (org_id::text = current_setting('app.current_org_id', true))
    WITH CHECK (org_id::text = current_setting('app.current_org_id', true));

-- Exact app_tenant grants (migration 63 convention): request traffic claims a
-- capability once and reads its own org's markers. A claimed marker is never
-- updated or deleted on the request path, so those verbs are withheld.
REVOKE ALL PRIVILEGES ON work_context_replay FROM app_tenant;
GRANT SELECT, INSERT ON work_context_replay TO app_tenant;

-- The control plane performs the cross-tenant retention deletes, so it holds
-- SELECT/INSERT/DELETE (migration 67 convention; default privileges grant it
-- nothing). No role gets UPDATE: a consumed marker is immutable.
GRANT SELECT, INSERT, DELETE ON work_context_replay TO app_control_plane;
