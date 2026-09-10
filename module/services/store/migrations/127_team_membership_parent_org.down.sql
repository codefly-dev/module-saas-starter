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

-- team_membership_quarantine is deliberately NOT dropped. It records memberships
-- this migration deleted, and `up` never restores them: dropping the table on
-- rollback would destroy the only evidence of what was removed, which is the one
-- thing the record exists to prevent. It has no foreign keys precisely so it can
-- outlive the schema that produced it.
