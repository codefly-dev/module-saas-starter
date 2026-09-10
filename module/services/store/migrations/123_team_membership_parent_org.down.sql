-- Quarantined rows are not restored: they were removed because no parent
-- organization membership authorized them, and that is still true.
ALTER TABLE team_members
    ADD CONSTRAINT team_members_team_id_fkey
        FOREIGN KEY (team_id) REFERENCES teams (id) ON DELETE CASCADE;

ALTER TABLE team_members
    DROP CONSTRAINT team_members_parent_org_membership_fkey,
    DROP CONSTRAINT team_members_team_org_fkey;

DROP INDEX idx_team_members_org_user;

ALTER TABLE team_members DROP COLUMN org_id;

ALTER TABLE teams DROP CONSTRAINT teams_id_org_id_key;

DROP TABLE team_membership_quarantine;
