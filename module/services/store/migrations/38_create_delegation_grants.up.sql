-- delegation_grants — the M7 escalation-flow + pattern-grant table.
--
-- Two-shape model:
--
-- 1. **Synchronous escalation grants**: an actor (typically an
--    agent) requests authority to perform a specific action on a
--    specific resource; a grantor approves or denies. Each row is
--    one request → one decision. status transitions:
--
--        pending → approved  (grantor approved; token minted)
--                ↘ denied    (grantor refused)
--                ↘ expired   (no decision within timeout)
--                ↘ cancelled (requestor or admin withdrew)
--
-- 2. **Pattern grants** (M8): a grantor pre-authorizes a class of
--    future calls — "approve any auto-merge for repo:codefly/* for
--    the next hour, max 100 uses". status='active' immediately;
--    auto-issues tokens for matching synchronous requests.
--
-- The same table holds both shapes; `kind` discriminates. M7 only
-- uses kind='one_shot'; M8 adds 'pattern'.
CREATE TABLE IF NOT EXISTS "delegation_grants" (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,

    -- Who's requesting (the actor needing authority).
    actor_principal_id UUID NOT NULL REFERENCES principals(id),

    -- Who's granting authority. NULL while the grant is still
    -- pending and no specific grantor has been routed to. For
    -- pattern grants, the grantor is set at create time (the
    -- principal who pre-authorized the pattern).
    grantor_principal_id UUID REFERENCES principals(id),

    -- What's being authorized.
    action TEXT NOT NULL,
    resource TEXT,
    resource_id TEXT,

    -- The actor's reason. REQUIRED — empty justifications turn
    -- approval into rubber-stamping. CHECK enforces non-empty
    -- on insert.
    justification TEXT NOT NULL CHECK (length(trim(justification)) > 0),

    -- Free-form context the requestor passed (PR labels,
    -- ci_status, etc.). Surfaced in the grantor's approval UI.
    request_context JSONB NOT NULL DEFAULT '{}'::jsonb,

    -- Risk classification (mirrors the manifest's risk_levels).
    -- Used for prioritizing the approval queue UI; not enforced
    -- in policy here (that's the gateway's tool policy).
    risk_level TEXT NOT NULL DEFAULT 'low'
        CHECK (risk_level IN ('low', 'medium', 'high', 'critical')),

    -- Lifecycle.
    kind TEXT NOT NULL DEFAULT 'one_shot'
        CHECK (kind IN ('one_shot', 'pattern')),
    status TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'approved', 'denied', 'expired', 'cancelled', 'active')),
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    decided_at TIMESTAMP WITH TIME ZONE,
    decision_reason TEXT,
    expires_at TIMESTAMP WITH TIME ZONE NOT NULL,

    -- For pattern grants (M8). Reserved here so M8 doesn't
    -- require a follow-up migration.
    action_pattern TEXT,
    resource_pattern TEXT,
    max_uses INT,
    use_count INT NOT NULL DEFAULT 0,

    -- The minted scoped-auth token's id, for audit correlation.
    -- Set when status=approved and a token has been minted.
    minted_token_id TEXT,

    -- Idempotency: same actor making the same request within a
    -- short window should resolve to the same row. Caller-
    -- supplied; gateway derives from a hash of (actor, action,
    -- resource, justification) so retries dedupe.
    request_hash TEXT,
    UNIQUE (org_id, request_hash)
);

-- Pending requests in an org, newest first — the approval queue UI's
-- primary read path.
CREATE INDEX IF NOT EXISTS delegation_grants_pending_idx
    ON delegation_grants (org_id, created_at DESC)
    WHERE status = 'pending';

-- Active pattern grants for runtime auto-approval matching.
CREATE INDEX IF NOT EXISTS delegation_grants_active_pattern_idx
    ON delegation_grants (org_id, action_pattern, resource_pattern)
    WHERE kind = 'pattern' AND status = 'active';

-- Audit lookup: "everything actor X has been granted recently".
CREATE INDEX IF NOT EXISTS delegation_grants_actor_idx
    ON delegation_grants (actor_principal_id, created_at DESC);

-- Audit lookup: "everything grantor X has approved recently".
CREATE INDEX IF NOT EXISTS delegation_grants_grantor_idx
    ON delegation_grants (grantor_principal_id, decided_at DESC)
    WHERE grantor_principal_id IS NOT NULL;

-- Postgres NOTIFY hook so the streaming WaitForDelegation RPC can
-- wake up on state changes without polling. The RPC handler
-- LISTENs to a channel named after the request id; the trigger
-- fires NOTIFY on every UPDATE that changes status.
CREATE OR REPLACE FUNCTION delegation_grants_notify_decided()
RETURNS TRIGGER AS $$
BEGIN
    IF NEW.status IS DISTINCT FROM OLD.status THEN
        PERFORM pg_notify(
            'delegation_grants:' || NEW.id::text,
            json_build_object(
                'id', NEW.id,
                'status', NEW.status,
                'decided_at', NEW.decided_at,
                'reason', COALESCE(NEW.decision_reason, '')
            )::text
        );
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER delegation_grants_notify_decided_tr
    AFTER UPDATE ON delegation_grants
    FOR EACH ROW EXECUTE FUNCTION delegation_grants_notify_decided();

COMMENT ON TABLE delegation_grants IS
    'M7+M8 escalation grants. one_shot: agent requests, grantor approves. pattern: pre-authorized class of future calls. NOTIFY trigger wakes WaitForDelegation streams on state change.';
