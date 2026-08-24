-- M9 audit views over delegation_grants.
--
-- These views are read-only aggregates. They live here (not in
-- the application's read models) because:
--
--   1. Operators / SRE want SQL access to the approval queue and
--      pattern-grant burn rate without going through the API.
--   2. Compliance audits need stable view names that survive
--      column renames in delegation_grants — the views give us a
--      contractual surface to iterate the underlying schema
--      without breaking external consumers.
--   3. The api package can SELECT from these via plain pgxpool;
--      no extra Go code path needed for routine "show me what
--      happened today" queries.
--
-- All views are RLS-enabled by inheritance (they query
-- delegation_grants which has RLS). Cross-org reads still
-- require the platform-admin context (WithBypass).

-- ---------------------------------------------------------------
-- delegation_grants_recent — last 7 days, denormalized.
--
-- Joins principals so the actor / grantor names are visible
-- without a follow-up query. Used by the admin dashboard's
-- "recent activity" panel and the audit-log query path as the
-- canonical "what happened" feed.
-- ---------------------------------------------------------------
CREATE OR REPLACE VIEW delegation_grants_recent AS
SELECT
    g.id,
    g.org_id,
    g.kind,
    g.status,
    g.action,
    g.resource,
    g.resource_id,
    g.justification,
    g.risk_level,
    g.created_at,
    g.decided_at,
    g.expires_at,
    g.decision_reason,
    g.minted_token_id,
    -- via_pattern caveat (M8 auto-approves) lifted from the
    -- request_context jsonb to a top-level column so SQL
    -- queries don't need to know the JSONB key.
    g.request_context->>'via_pattern' AS via_pattern,
    g.actor_principal_id,
    actor.display_name           AS actor_display_name,
    actor.kind                   AS actor_kind,
    actor.agent_identifier       AS actor_agent_identifier,
    g.grantor_principal_id,
    grantor.display_name         AS grantor_display_name,
    grantor.kind                 AS grantor_kind
FROM delegation_grants g
JOIN principals actor ON actor.id = g.actor_principal_id
LEFT JOIN principals grantor ON grantor.id = g.grantor_principal_id
WHERE g.created_at > CURRENT_TIMESTAMP - INTERVAL '7 days';

COMMENT ON VIEW delegation_grants_recent IS
    'M9 audit view: last 7 days of delegation grants with actor/grantor names denormalized. Inherits RLS from delegation_grants.';

-- ---------------------------------------------------------------
-- delegation_pattern_usage — pattern grants with burn-rate.
--
-- Surfaces "how close to exhaustion" for each active pattern so
-- operators can spot patterns being abused (use_count climbing
-- fast) or that should be expanded (consistently exhausted with
-- legitimate retries). NULL max_uses = unlimited; the
-- usage_pct column is NULL in that case.
-- ---------------------------------------------------------------
CREATE OR REPLACE VIEW delegation_pattern_usage AS
SELECT
    g.id,
    g.org_id,
    g.actor_principal_id,
    actor.display_name AS actor_display_name,
    g.grantor_principal_id,
    grantor.display_name AS grantor_display_name,
    g.action_pattern,
    g.resource_pattern,
    g.use_count,
    g.max_uses,
    CASE
      WHEN g.max_uses IS NULL THEN NULL
      WHEN g.max_uses = 0     THEN NULL
      ELSE ROUND(100.0 * g.use_count / g.max_uses, 1)
    END                AS usage_pct,
    g.status,
    g.created_at,
    g.expires_at
FROM delegation_grants g
JOIN principals actor ON actor.id = g.actor_principal_id
LEFT JOIN principals grantor ON grantor.id = g.grantor_principal_id
WHERE g.kind = 'pattern';

COMMENT ON VIEW delegation_pattern_usage IS
    'M9 audit view: pattern-grant burn-rate (use_count / max_uses). usage_pct is NULL when max_uses is null/zero.';

-- ---------------------------------------------------------------
-- delegation_stats_daily — counts by org / status / risk.
--
-- Groups by day so dashboards can plot "approvals over time".
-- Only includes the last 90 days; production traffic at scale
-- can grow this view large enough that an unbounded scan
-- becomes painful. Operators wanting longer windows pull
-- straight from delegation_grants.
-- ---------------------------------------------------------------
CREATE OR REPLACE VIEW delegation_stats_daily AS
SELECT
    org_id,
    DATE_TRUNC('day', created_at) AS day,
    status,
    risk_level,
    COUNT(*)                       AS count,
    -- Auto-approves pulled out as their own dimension because
    -- they're operationally distinct (no human approver) and
    -- the dashboards highlight them separately.
    COUNT(*) FILTER (
        WHERE request_context ? 'via_pattern'
    )                              AS auto_approved_count
FROM delegation_grants
WHERE created_at > CURRENT_TIMESTAMP - INTERVAL '90 days'
GROUP BY org_id, DATE_TRUNC('day', created_at), status, risk_level;

COMMENT ON VIEW delegation_stats_daily IS
    'M9 audit view: 90-day delegation counts grouped by day/status/risk. auto_approved_count splits out via_pattern grants.';
