-- Team membership becomes a child of organization membership, enforced by the
-- database rather than by whichever writer happens to be in the call path.
--
-- team_members linked a team to a row in the global users table; nothing tied
-- the member to the team's parent organization. A caller authorized to
-- administer a team could therefore install any user in the system as a team
-- administrator. The RLS policy scopes which team's rows are reachable, which
-- says nothing about whether the proposed member belongs to that organization.
--
-- The invariant is two composite foreign keys, so it holds for every writer
-- including direct SQL: PostgreSQL performs referential integrity checks with
-- row security bypassed, so neither FORCE ROW LEVEL SECURITY nor the runtime
-- role (app_tenant, app_control_plane, the migration principal) changes the
-- outcome.
--
--   (team_id, org_id) -> teams (id, org_id)
--       org_id cannot disagree with the team's own organization, so no writer
--       can offer an organization of its own choosing as proof of anything.
--   (org_id, user_id) -> organization_members (org_id, user_id) ON DELETE CASCADE
--       a team membership cannot exist without a live parent membership, and
--       removing the parent removes it in the same transaction.
--
-- The insert's FOR KEY SHARE lock on the organization_members row also
-- serializes a team write against a concurrent organization removal: whichever
-- transaction commits second observes the other, so neither ordering leaves an
-- orphan behind.

-- Invalid relations that predate the invariant are recorded here and removed.
-- They are never repaired by manufacturing the missing parent membership: the
-- organization never granted it, and inventing one would turn a data defect
-- into an authorization grant. No foreign keys, deliberately — the record has
-- to outlive the rows it describes.
CREATE TABLE IF NOT EXISTS "team_membership_quarantine" (
    team_id        UUID NOT NULL,
    org_id         UUID NOT NULL,
    user_id        UUID NOT NULL,
    role           TEXT NOT NULL,
    joined_at      TIMESTAMP WITH TIME ZONE,
    quarantined_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    reason         TEXT NOT NULL,
    PRIMARY KEY (team_id, user_id, quarantined_at)
);

-- Request traffic gets no grant at all, so this policy governs nothing today.
-- It is here because a table with a tenant column and no policy is
-- indistinguishable from one whose isolation was forgotten, and because the
-- day someone does grant a read, the boundary is already in place.
ALTER TABLE team_membership_quarantine ENABLE ROW LEVEL SECURITY;
ALTER TABLE team_membership_quarantine FORCE  ROW LEVEL SECURITY;

-- The table outlives a rollback (see the down migration), so re-applying this
-- migration meets an existing policy.
DROP POLICY IF EXISTS team_membership_quarantine_tenant ON team_membership_quarantine;

CREATE POLICY team_membership_quarantine_tenant ON team_membership_quarantine
    USING (org_id::text = current_setting('app.current_org_id', true))
    WITH CHECK (org_id::text = current_setting('app.current_org_id', true));

GRANT SELECT ON team_membership_quarantine TO app_control_plane;

ALTER TABLE teams ADD CONSTRAINT teams_id_org_id_key UNIQUE (id, org_id);

ALTER TABLE team_members ADD COLUMN org_id UUID;

UPDATE team_members m
SET org_id = t.org_id
FROM teams t
WHERE t.id = m.team_id;

WITH orphan AS (
    DELETE FROM team_members m
    WHERE NOT EXISTS (
        SELECT 1 FROM organization_members o
        WHERE o.org_id = m.org_id
          AND o.user_id = m.user_id
    )
    RETURNING m.team_id, m.org_id, m.user_id, m.role, m.joined_at
)
INSERT INTO team_membership_quarantine (team_id, org_id, user_id, role, joined_at, reason)
SELECT team_id, org_id, user_id, role, joined_at,
       'no parent-organization membership when migration 123 introduced the invariant'
FROM orphan;

-- ADD COLUMN took an ACCESS EXCLUSIVE lock that this file holds until it
-- commits, so the backfill above and this scan are a write outage on a large
-- team_members. The two constraint validations are deliberately not part of
-- it — see migration 124.
ALTER TABLE team_members ALTER COLUMN org_id SET NOT NULL;

-- The referencing side of an ON DELETE CASCADE needs its own index, or every
-- organization-member removal degrades to a sequential scan of team_members.
CREATE INDEX idx_team_members_org_user ON team_members(org_id, user_id);

ALTER TABLE team_members
    ADD CONSTRAINT team_members_team_org_fkey
        FOREIGN KEY (team_id, org_id) REFERENCES teams (id, org_id) ON DELETE CASCADE NOT VALID,
    ADD CONSTRAINT team_members_parent_org_membership_fkey
        FOREIGN KEY (org_id, user_id) REFERENCES organization_members (org_id, user_id) ON DELETE CASCADE NOT VALID;

-- Declared NOT VALID, and validated by migration 124 rather than here. Each
-- migration file runs as one implicit transaction, so validating in this file
-- would hold the ACCESS EXCLUSIVE lock above across two more full scans of
-- team_members. A NOT VALID constraint still rejects every new write, so the
-- invariant is live from this migration on; 124 only settles the rows that
-- predate it, under a lock that does not block reads or writes.

-- The single-column team reference is subsumed by the composite one.
ALTER TABLE team_members DROP CONSTRAINT team_members_team_id_fkey;
