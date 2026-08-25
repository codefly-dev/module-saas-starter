-- saas/approvals/v1 — the general Approval primitive (issue #232).
--
-- Gate any action behind "pause until an authorized decision (or timeout /
-- escalation) arrives, then resume". Two tables mirror the
-- delegation_grants + actor_chain_journal split:
--
--   * approval_requests  — the mutable head. One row per gated action; state
--                          transitions pending → approved | denied | expired |
--                          escalated | cancelled.
--   * approval_decisions — append-only. One row per approver decision; an
--                          immutable trigger rejects UPDATE/DELETE. Quorum is a
--                          count of DISTINCT approve rows ≥ the request's quorum,
--                          and UNIQUE (request_id, decider) blocks double-voting.
--
-- Both are org-scoped and RLS-isolated exactly like actor_chain_journal
-- (migration 99): visible/writable only when acting in the row's org; the
-- control plane reaches rows under its BYPASSRLS role (app_control_plane).

CREATE TABLE approval_requests (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id        UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,

    -- What is being gated. resource/action mirror the method_policy vocabulary
    -- ("entitlement_override" / "grant"); subject carries the typed payload of
    -- the specific thing under approval.
    resource      TEXT  NOT NULL,
    action        TEXT  NOT NULL,
    subject       JSONB NOT NULL DEFAULT '{}'::jsonb,

    -- Opaque actor id of the requester (a principal or user id, matching the
    -- actor-chain actor identity). Validated at the handler boundary, not here.
    requested_by  TEXT  NOT NULL,

    -- Quorum: the number of DISTINCT approve decisions required. The rest of the
    -- policy (explicit approver_set, allow_self, decide_permission) lives in the
    -- policy JSONB so it can grow without a migration.
    quorum        INT   NOT NULL DEFAULT 1 CHECK (quorum >= 1),
    policy        JSONB NOT NULL DEFAULT '{}'::jsonb,

    state         TEXT  NOT NULL DEFAULT 'pending'
        CHECK (state IN ('pending', 'approved', 'denied',
                         'expired', 'escalated', 'cancelled')),

    -- Outbox target for the resume: {mode, job_kind, payload}. mode selects
    -- borrow-capability (mint a scoped grant, requester replays) vs system-outbox
    -- (the system actor performs the mutation) — see APPROVALS_DESIGN.md §7.
    resume_ref    JSONB NOT NULL DEFAULT '{}'::jsonb,

    -- Decision window (before quorum). NULL = no timeout / no escalation.
    expires_at    TIMESTAMPTZ,
    escalate_at   TIMESTAMPTZ,

    decision_reason TEXT,
    decided_at    TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- The approval-queue read path: pending requests in an org, newest first.
CREATE INDEX approval_requests_pending_idx
    ON approval_requests (org_id, created_at DESC)
    WHERE state = 'pending';

CREATE TABLE approval_decisions (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    request_id   UUID NOT NULL REFERENCES approval_requests(id) ON DELETE CASCADE,
    -- Denormalized for RLS: a decision is isolated to its request's org.
    org_id       UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,

    decider      TEXT NOT NULL,
    decision     TEXT NOT NULL CHECK (decision IN ('approve', 'deny')),
    reason       TEXT,

    -- Links the decision to the immutable actor chain — "who approved on whose
    -- behalf" — reusing the delegation_grant_id pattern (migration 99). NULL
    -- until an approval is also backed by a minted delegation grant.
    delegation_grant_id UUID REFERENCES delegation_grants(id),

    decided_at   TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,

    -- One decision per approver per request: makes distinct-approver quorum
    -- enforceable in SQL and blocks double-voting without application logic.
    UNIQUE (request_id, decider)
);

-- Counting distinct approves toward quorum.
CREATE INDEX approval_decisions_request_idx
    ON approval_decisions (request_id, decision);

-- Append-only enforcement: a decision, once made, is evidence.
CREATE OR REPLACE FUNCTION approval_decisions_immutable() RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'approval_decisions is append-only';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER approval_decisions_no_update
    BEFORE UPDATE ON approval_decisions FOR EACH ROW
    EXECUTE FUNCTION approval_decisions_immutable();
CREATE TRIGGER approval_decisions_no_delete
    BEFORE DELETE ON approval_decisions FOR EACH ROW
    EXECUTE FUNCTION approval_decisions_immutable();

-- Tenant RLS, mirroring actor_chain_journal (migration 99). The FOR ALL policy
-- admits every verb so the rls-migration-gate stays satisfied; the explicit
-- GRANTs below are what actually restrict each role's verbs. The control plane
-- reaches rows under its BYPASSRLS role.
ALTER TABLE approval_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE approval_requests FORCE  ROW LEVEL SECURITY;
CREATE POLICY approval_requests_tenant ON approval_requests
    USING (org_id::text = current_setting('app.current_org_id', true))
    WITH CHECK (org_id::text = current_setting('app.current_org_id', true));

ALTER TABLE approval_decisions ENABLE ROW LEVEL SECURITY;
ALTER TABLE approval_decisions FORCE  ROW LEVEL SECURITY;
CREATE POLICY approval_decisions_tenant ON approval_decisions
    USING (org_id::text = current_setting('app.current_org_id', true))
    WITH CHECK (org_id::text = current_setting('app.current_org_id', true));

-- Explicit per-relation grants (required from migration 61). The request head is
-- read/created/transitioned; decisions are append-only (no UPDATE/DELETE).
GRANT SELECT, INSERT, UPDATE ON approval_requests  TO app_tenant;
GRANT SELECT, INSERT, UPDATE ON approval_requests  TO app_control_plane;
GRANT SELECT, INSERT         ON approval_decisions TO app_tenant;
GRANT SELECT, INSERT         ON approval_decisions TO app_control_plane;

-- The timeout/escalation sweeper runs as a delayed Job handler under the job
-- worker role (app_job_worker, migration 72), per-org via WithOrgTx (so RLS
-- applies — app_job_worker has no BYPASSRLS). It reads and transitions the head
-- row (SELECT ... FOR UPDATE + UPDATE), so it needs those verbs on
-- approval_requests. Same pattern as analytics_deliveries (migration 81).
-- Without this grant the wired sweeper fails closed.
GRANT SELECT, UPDATE ON approval_requests TO app_job_worker;

COMMENT ON TABLE approval_requests IS
    'saas/approvals/v1 head row. Gates an action until quorum distinct approve decisions arrive, or a timeout/escalation sweeper flips it. Resumes the gated action via the outbox (resume_ref).';
COMMENT ON TABLE approval_decisions IS
    'saas/approvals/v1 append-only decision log. Quorum = distinct approve rows >= approval_requests.quorum. UNIQUE (request_id, decider) blocks double-voting.';
