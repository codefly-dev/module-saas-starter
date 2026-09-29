-- Migration 11: the monthly audit_events_YYYY_MM partitions had no RLS of
-- their own. PostgreSQL applies a partitioned table's policies only to a query
-- that names the parent; a query that names a partition is checked against the
-- partition's own policies, and these had none. The store's read-only login
-- holds SELECT on every table, and a default privilege on every future one, so
-- it read every organization's audit rows straight out of a partition while the
-- parent returned none.
--
-- Every partition now enables and forces row-level security and carries the
-- parent's policies. They are copied from the catalog, so a partition's roles,
-- commands and USING / WITH CHECK expressions are the parent's own rather than a
-- second hand-written copy that could drift from it. A row written or read
-- through the parent is unaffected: PostgreSQL checks the parent's policies for
-- it and never the partition's.

-- Owned by the migration principal, like the partitions it alters, and invoked
-- with that authority only by audit_events_ensure_partition (SECURITY DEFINER)
-- and by migrations. Nobody else may execute it.
--
-- Both functions list pg_temp last in their search_path and name every catalog
-- relation by schema. PostgreSQL searches a session's temporary schema FIRST for
-- relation and type names unless search_path lists it, so a caller could
-- otherwise shadow pg_policy or pg_class with a temporary table and make the
-- drift check below report a partition as already secured.
CREATE FUNCTION public.audit_events_secure_partition(target regclass) RETURNS void
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $$
DECLARE
    parent    CONSTANT regclass := 'public.audit_events'::regclass;
    pol       record;
    role_list text;
    drifted   boolean;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_inherits WHERE inhrelid = target AND inhparent = parent) THEN
        RAISE EXCEPTION '% is not a partition of %', target, parent;
    END IF;

    IF NOT (SELECT relrowsecurity FROM pg_catalog.pg_class WHERE oid = target) THEN
        EXECUTE format('ALTER TABLE %s ENABLE ROW LEVEL SECURITY', target);
    END IF;
    IF NOT (SELECT relforcerowsecurity FROM pg_catalog.pg_class WHERE oid = target) THEN
        EXECUTE format('ALTER TABLE %s FORCE ROW LEVEL SECURITY', target);
    END IF;

    -- Rewrite the partition's policies only when they differ from the parent's,
    -- so a partition already in step takes no lock here.
    SELECT EXISTS (
        (SELECT polname, polpermissive, polcmd, polroles,
                pg_get_expr(polqual, polrelid), pg_get_expr(polwithcheck, polrelid)
           FROM pg_catalog.pg_policy WHERE polrelid = parent
         EXCEPT
         SELECT polname, polpermissive, polcmd, polroles,
                pg_get_expr(polqual, polrelid), pg_get_expr(polwithcheck, polrelid)
           FROM pg_catalog.pg_policy WHERE polrelid = target)
        UNION ALL
        (SELECT polname, polpermissive, polcmd, polroles,
                pg_get_expr(polqual, polrelid), pg_get_expr(polwithcheck, polrelid)
           FROM pg_catalog.pg_policy WHERE polrelid = target
         EXCEPT
         SELECT polname, polpermissive, polcmd, polroles,
                pg_get_expr(polqual, polrelid), pg_get_expr(polwithcheck, polrelid)
           FROM pg_catalog.pg_policy WHERE polrelid = parent)
    ) INTO drifted;
    IF NOT drifted THEN
        RETURN;
    END IF;

    FOR pol IN SELECT polname FROM pg_catalog.pg_policy WHERE polrelid = target LOOP
        EXECUTE format('DROP POLICY %I ON %s', pol.polname, target);
    END LOOP;
    FOR pol IN
        SELECT polname, polpermissive, polcmd, polroles,
               pg_get_expr(polqual, polrelid) AS qual,
               pg_get_expr(polwithcheck, polrelid) AS with_check
          FROM pg_catalog.pg_policy WHERE polrelid = parent ORDER BY polname
    LOOP
        -- Role 0 in polroles is PUBLIC.
        SELECT string_agg(CASE WHEN grantee.role_oid = 0 THEN 'PUBLIC' ELSE quote_ident(r.rolname) END, ', ' ORDER BY grantee.ordinal)
          INTO role_list
          FROM unnest(pol.polroles) WITH ORDINALITY AS grantee(role_oid, ordinal)
          LEFT JOIN pg_catalog.pg_roles r ON r.oid = grantee.role_oid;
        EXECUTE format('CREATE POLICY %I ON %s AS %s FOR %s TO %s%s%s',
            pol.polname,
            target,
            CASE WHEN pol.polpermissive THEN 'PERMISSIVE' ELSE 'RESTRICTIVE' END,
            CASE pol.polcmd WHEN 'r' THEN 'SELECT' WHEN 'a' THEN 'INSERT' WHEN 'w' THEN 'UPDATE' WHEN 'd' THEN 'DELETE' ELSE 'ALL' END,
            role_list,
            CASE WHEN pol.qual IS NULL THEN '' ELSE format(' USING (%s)', pol.qual) END,
            CASE WHEN pol.with_check IS NULL THEN '' ELSE format(' WITH CHECK (%s)', pol.with_check) END);
    END LOOP;
END;
$$;

REVOKE ALL ON FUNCTION public.audit_events_secure_partition(regclass) FROM PUBLIC;

-- A partition is secured in the same transaction that creates it, and an
-- existing one is brought back in step whenever it is ensured, so none is ever
-- readable by name without the parent's policies.
CREATE OR REPLACE FUNCTION public.audit_events_ensure_partition(month date) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $$
DECLARE
    start_date DATE := date_trunc('month', month)::date;
    end_date   DATE := (date_trunc('month', month) + INTERVAL '1 month')::date;
    part_name  TEXT := 'audit_events_' || to_char(start_date, 'YYYY_MM');
BEGIN
    IF to_regclass('public.' || part_name) IS NULL THEN
        EXECUTE format(
            'CREATE TABLE public.%I PARTITION OF public.audit_events FOR VALUES FROM (%L) TO (%L)',
            part_name, start_date, end_date
        );
    END IF;
    PERFORM public.audit_events_secure_partition(to_regclass('public.' || quote_ident(part_name)));
END;
$$;

DO $partitions$
DECLARE
    part regclass;
BEGIN
    FOR part IN SELECT inhrelid::regclass FROM pg_catalog.pg_inherits WHERE inhparent = 'public.audit_events'::regclass LOOP
        PERFORM public.audit_events_secure_partition(part);
    END LOOP;
END
$partitions$;
