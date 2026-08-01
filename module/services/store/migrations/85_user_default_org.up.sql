-- Deterministic session tenant. default_org_id is the explicit organization a
-- returning user lands on, replacing the identity resolver's former "most
-- recent membership wins" heuristic. NULL is a legitimate value: a user in zero
-- or several orgs with no recorded preference resolves to an explicit orgless
-- session rather than a guessed tenant. ON DELETE SET NULL so removing an org
-- clears the pointer instead of orphaning it.
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS default_org_id UUID
        REFERENCES organizations(id) ON DELETE SET NULL;

-- Backfill the only unambiguous case: a user who belongs to exactly one
-- organization has no other possible default. Users in zero or multiple orgs
-- are deliberately left NULL — there is no non-arbitrary choice to make, and the
-- resolver models that state explicitly.
UPDATE users u
   SET default_org_id = m.org_id
  FROM organization_members m
 WHERE u.uuid = m.user_id
   AND u.default_org_id IS NULL
   AND (SELECT COUNT(*) FROM organization_members m2 WHERE m2.user_id = u.uuid) = 1;
