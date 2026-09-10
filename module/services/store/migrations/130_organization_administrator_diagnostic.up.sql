-- Inventory the organizations whose administrative authority is already
-- inconsistent, before anything starts enforcing that it cannot become so.
--
-- Two states exist in historical data and neither can be repaired by a
-- migration. An organization with no owner or admin membership has nobody who
-- can administer it, and the only way to "fix" that is to grant an arbitrary
-- user administrative authority — a privilege escalation performed by a deploy,
-- with no operator deciding who. An organization whose `owner_id` is not an
-- administrative member is a disagreement between the owner of record and who
-- can actually administer it; writing either side to match the other silently
-- moves authority. So both are reported, and neither is touched.
--
-- The report matters ahead of enforcement rather than after it. An invariant
-- that rejects mutations "leaving an organization without an administrator"
-- must not also reject the mutations an operator needs in order to clean up an
-- organization that is already in that state, and nobody can size that backlog
-- without this inventory.
--
-- Scope note: this covers the organization-level findings only. The
-- team-membership side of membership integrity — a team member with no parent
-- organization membership — is owned by the parent-organization relation work
-- and quarantines its own rows; this migration deliberately does not duplicate
-- that scan or that repair.
CREATE TABLE IF NOT EXISTS "membership_integrity_findings" (
    id          UUID DEFAULT gen_random_uuid() PRIMARY KEY,
    org_id      UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    finding     TEXT NOT NULL CHECK (finding IN (
                    'organization_without_administrator',
                    'owner_of_record_is_not_an_administrator'
                )),
    detail      JSONB NOT NULL,
    found_at    TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (org_id, finding)
);

-- Operator repair evidence, read through the control-plane boundary. Request
-- traffic gets no grant at all (migration 63's exact-grant model), so the
-- policy governs the control plane's own reads; it is present because a
-- tenant-columned table without one is indistinguishable from an unprotected
-- one.
ALTER TABLE membership_integrity_findings ENABLE ROW LEVEL SECURITY;
ALTER TABLE membership_integrity_findings FORCE  ROW LEVEL SECURITY;

CREATE POLICY membership_integrity_findings_tenant ON membership_integrity_findings
    USING (org_id::text = current_setting('app.current_org_id', true))
    WITH CHECK (org_id::text = current_setting('app.current_org_id', true));

GRANT SELECT, INSERT, UPDATE, DELETE ON membership_integrity_findings TO app_control_plane;

-- The scan is a function rather than inline DML so an operator can re-run it
-- after repairing organizations and see the backlog shrink, instead of trusting
-- a one-shot snapshot taken at deploy. It is a plain (invoker-rights) function:
-- an inventory has to see every organization, and the one role that may run it
-- already spans tenants through BYPASSRLS, so SECURITY DEFINER would add
-- privilege without adding capability.
--
-- Re-running is safe and non-destructive. A finding that still holds keeps its
-- original found_at (the conflict target is the pair, and the DO UPDATE only
-- refreshes detail), and a finding that has since been repaired is deleted, so
-- the table always reads as the current backlog rather than an append-only
-- history. Returns the number of findings outstanding afterwards.
CREATE FUNCTION public.record_membership_integrity_findings()
RETURNS integer
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $function$
DECLARE
    outstanding integer;
BEGIN
    WITH current_findings AS (
        SELECT
            o.id AS org_id,
            'organization_without_administrator' AS finding,
            jsonb_build_object('owner_id', o.owner_id) AS detail
        FROM organizations o
        WHERE NOT EXISTS (
            SELECT 1 FROM organization_members om
            WHERE om.org_id = o.id AND om.role IN ('owner', 'admin')
        )
        UNION ALL
        SELECT
            o.id,
            'owner_of_record_is_not_an_administrator',
            jsonb_build_object(
                'owner_id', o.owner_id,
                'membership_role', (
                    SELECT om.role FROM organization_members om
                    WHERE om.org_id = o.id AND om.user_id = o.owner_id
                )
            )
        FROM organizations o
        WHERE NOT EXISTS (
            SELECT 1 FROM organization_members om
            WHERE om.org_id = o.id
              AND om.user_id = o.owner_id
              AND om.role IN ('owner', 'admin')
        )
    ),
    resolved AS (
        DELETE FROM membership_integrity_findings f
        WHERE NOT EXISTS (
            SELECT 1 FROM current_findings c
            WHERE c.org_id = f.org_id AND c.finding = f.finding
        )
    )
    INSERT INTO membership_integrity_findings (org_id, finding, detail)
    SELECT org_id, finding, detail FROM current_findings
    ON CONFLICT (org_id, finding) DO UPDATE SET detail = EXCLUDED.detail;

    SELECT count(*) INTO outstanding FROM membership_integrity_findings;
    RETURN outstanding;
END
$function$;

REVOKE ALL ON FUNCTION public.record_membership_integrity_findings() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.record_membership_integrity_findings() TO app_control_plane;

SELECT public.record_membership_integrity_findings();
