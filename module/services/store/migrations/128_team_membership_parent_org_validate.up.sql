-- Settle the rows that predate migration 127's parent-organization invariant.
--
-- 127 added both composite foreign keys NOT VALID. That already rejects every
-- new write, so the invariant has been live since 127; what NOT VALID leaves
-- open is only whether rows written before it satisfy the constraint. 127
-- deleted the ones that did not, so these scans are expected to find nothing.
--
-- They live in their own migration because each migration file runs as one
-- implicit transaction. In 127 these statements would have run under the ACCESS
-- EXCLUSIVE lock that file's ADD COLUMN takes and holds to commit; alone here
-- they take SHARE UPDATE EXCLUSIVE, which does not block reads or writes. A
-- deployment whose team_members is too large to scan inside one release window
-- can hold this migration back without weakening anything.
ALTER TABLE team_members VALIDATE CONSTRAINT team_members_team_org_fkey;
ALTER TABLE team_members VALIDATE CONSTRAINT team_members_parent_org_membership_fkey;
