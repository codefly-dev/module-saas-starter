-- Phase 2C — RLS on teams + team_members.
--
-- teams has a non-null org_id — direct tenant policy.
-- team_members has no org_id — JOIN policy via teams (same shape
-- as webhook_deliveries → webhook_subscriptions in migration 27).

ALTER TABLE teams ENABLE ROW LEVEL SECURITY;
ALTER TABLE teams FORCE  ROW LEVEL SECURITY;

CREATE POLICY teams_tenant ON teams
    USING (
        org_id::text = current_setting('app.current_org_id', true)
        OR current_setting('app.bypass', true) = '1'
    )
    WITH CHECK (
        org_id::text = current_setting('app.current_org_id', true)
        OR current_setting('app.bypass', true) = '1'
    );

ALTER TABLE team_members ENABLE ROW LEVEL SECURITY;
ALTER TABLE team_members FORCE  ROW LEVEL SECURITY;

-- The JOIN re-applies teams's policy: a member row is visible iff
-- its parent team is visible to the current setting. EXISTS is
-- short-circuit and indexed on team_id, ~no perf cost.
CREATE POLICY team_members_tenant ON team_members
    USING (
        current_setting('app.bypass', true) = '1'
        OR EXISTS (
            SELECT 1 FROM teams t
            WHERE t.id = team_members.team_id
              AND t.org_id::text = current_setting('app.current_org_id', true)
        )
    )
    WITH CHECK (
        current_setting('app.bypass', true) = '1'
        OR EXISTS (
            SELECT 1 FROM teams t
            WHERE t.id = team_members.team_id
              AND t.org_id::text = current_setting('app.current_org_id', true)
        )
    );
