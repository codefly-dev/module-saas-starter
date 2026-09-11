-- PostgreSQL cannot return a validated constraint to NOT VALID in place, so the
-- pair is dropped and re-added. Enforcement of new writes is never lifted: the
-- re-added constraints reject exactly what they rejected before, and only the
-- guarantee about pre-127 rows is given up.
ALTER TABLE team_members
    DROP CONSTRAINT team_members_team_org_fkey,
    DROP CONSTRAINT team_members_parent_org_membership_fkey;

ALTER TABLE team_members
    ADD CONSTRAINT team_members_team_org_fkey
        FOREIGN KEY (team_id, org_id) REFERENCES teams (id, org_id) ON DELETE CASCADE NOT VALID,
    ADD CONSTRAINT team_members_parent_org_membership_fkey
        FOREIGN KEY (org_id, user_id) REFERENCES organization_members (org_id, user_id) ON DELETE CASCADE NOT VALID;
