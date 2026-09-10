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

-- The natural key is the pair, not a surrogate: a finding IS the fact that this
-- organization is in this state, and there is never more than one of them.
-- Migration 13 dropped every `gen_random_uuid()` default on purpose (the
-- business layer issues uuid v7 explicitly), and a table written only by the
-- scan below has no business reintroducing one for a column nothing reads.
CREATE TABLE IF NOT EXISTS "membership_integrity_findings" (
    org_id      UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    finding     TEXT NOT NULL CHECK (finding IN (
                    'organization_without_administrator',
                    'owner_of_record_is_not_an_administrator'
                )),
    detail      JSONB NOT NULL,
    found_at    TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (org_id, finding)
);

-- Operator repair evidence. No role actually reaches these rows *through* this
-- policy: request traffic holds no grant at all (migration 63's exact-grant
-- model), and app_control_plane — the one role that does hold a grant — spans
-- organizations through BYPASSRLS, which is decided before any policy is
-- consulted. The policy is therefore nobody's access-control decision. It is
-- here because a tenant-columned table without one is indistinguishable from an
-- unprotected one, and because the authority inventory requires every tenant
-- relation to force RLS and carry a tenant-scoped policy.
ALTER TABLE membership_integrity_findings ENABLE ROW LEVEL SECURITY;
ALTER TABLE membership_integrity_findings FORCE  ROW LEVEL SECURITY;

CREATE POLICY membership_integrity_findings_tenant ON membership_integrity_findings
    USING (org_id::text = current_setting('app.current_org_id', true))
    WITH CHECK (org_id::text = current_setting('app.current_org_id', true));

GRANT SELECT, INSERT, UPDATE, DELETE ON membership_integrity_findings TO app_control_plane;

-- The scan is a function rather than inline DML so an operator can re-run it
-- after repairing organizations and see the backlog shrink, instead of trusting
-- a one-shot snapshot taken at deploy. `membership-integrity-scan` is that
-- operator's vehicle; it assumes app_control_plane, as this migration does
-- below. It is a plain (invoker-rights) function: the one role that may run it
-- already spans tenants through BYPASSRLS, so SECURITY DEFINER would add
-- privilege without adding capability.
--
-- Re-running is safe and non-destructive. A finding that still holds keeps its
-- original found_at (the conflict target is the pair, and the DO UPDATE only
-- refreshes detail), and a finding that has since been repaired is deleted, so
-- the table always reads as the current backlog rather than an append-only
-- history. Returns the number of findings outstanding afterwards, which is also
-- the number of organizations outstanding: the two findings are mutually
-- exclusive by construction, so no organization is ever counted twice.
CREATE FUNCTION public.record_membership_integrity_findings()
RETURNS integer
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $function$
DECLARE
    outstanding integer;
BEGIN
    -- An inventory has to see every organization, and organizations /
    -- organization_members are FORCE ROW LEVEL SECURITY — which subjects the
    -- table owner to their policies as well. Those policies scope to
    -- app.current_org_id and carry no bypass clause (a session setting must
    -- never grant RLS authority; the role-hardening suite asserts no live
    -- policy reads one). A caller that does not span organizations therefore
    -- reads zero rows, records nothing, and returns 0 — a result identical to a
    -- healthy platform and one nobody would think to question. Refuse instead.
    IF NOT EXISTS (
        SELECT 1 FROM pg_roles
        WHERE rolname = current_user AND (rolbypassrls OR rolsuper)
    ) THEN
        RAISE EXCEPTION
            'record_membership_integrity_findings() must run as a role that spans every organization; % does not, and would record an empty backlog',
            current_user
            USING HINT = 'assume the control plane first: SET ROLE app_control_plane';
    END IF;

    WITH current_findings AS (
        -- Nobody can administer this organization at all.
        SELECT
            o.id AS org_id,
            'organization_without_administrator' AS finding,
            jsonb_build_object(
                'owner_id', o.owner_id,
                'membership_role', (
                    SELECT om.role FROM organization_members om
                    WHERE om.org_id = o.id AND om.user_id = o.owner_id
                )
            ) AS detail
        FROM organizations o
        WHERE NOT EXISTS (
            SELECT 1 FROM organization_members om
            WHERE om.org_id = o.id AND om.role IN ('owner', 'admin')
        )
        UNION ALL
        -- Somebody can administer this organization, but it is not the owner of
        -- record. Restricted to organizations that have an administrator on
        -- purpose: an organization with none satisfies this predicate too (its
        -- owner is trivially not an administrator), and reporting both would
        -- count the same organization twice and inflate the backlog an operator
        -- is trying to size. The first finding is the stronger statement, and
        -- carries the owner's membership role in its own detail.
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
        WHERE EXISTS (
            SELECT 1 FROM organization_members om
            WHERE om.org_id = o.id AND om.role IN ('owner', 'admin')
        )
        AND NOT EXISTS (
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

-- Record the inventory now, so the backlog exists before anything enforces the
-- invariant. This migration runs under the store owner-connection, which is a
-- plain table owner: on managed Postgres it is neither superuser nor BYPASSRLS,
-- and FORCE ROW LEVEL SECURITY applies to the owner. Scanning as the owner
-- would see no organizations and write no findings, and the guard above turns
-- that into an error rather than a green deploy reporting an empty platform.
-- So assume the one role that spans organizations for the scan itself. If the
-- deploying principal is not a member of app_control_plane this fails loudly
-- here, which is the outcome to prefer.
SET ROLE app_control_plane;
SELECT public.record_membership_integrity_findings();
RESET ROLE;
