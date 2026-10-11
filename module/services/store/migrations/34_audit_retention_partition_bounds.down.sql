-- Reverts migration 34. Retention and provisioning give up its rules: what
-- retention drops depends again on the zone of the session that runs it and of
-- the one that created each partition, it accepts a cutoff the policy does not
-- allow, a partition being detached, or detached or renamed while it ran, is
-- dropped like any other, and neither retention nor provisioning bounds its
-- wait for a lock.
-- A new partition's bounds are again the creating session's midnights, and
-- data_retention_policies accepts a window that is not a positive number of days.
--
-- Each function is restored as the migration that last defined it left it --
-- audit_events_drop_partitions_before as migration 12 left it,
-- audit_events_ensure_partition and the one-argument
-- audit_events_secure_partition as migration 11 did, and
-- audit_events_secure_all_partitions as migration 13 did -- search_path
-- included. CREATE OR REPLACE replaces every SET clause, so lock_timeout,
-- DateStyle and TimeZone go with migration 34, and keeps owners and grants. The
-- functions migration 34 added are dropped once nothing calls them.

ALTER TABLE public.data_retention_policies
    DROP CONSTRAINT data_retention_policies_retention_days_positive;

CREATE OR REPLACE FUNCTION public.audit_events_drop_partitions_before(cutoff timestamp with time zone) RETURNS integer
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $_$
DECLARE
    part        RECORD;
    dropped     INT := 0;
    upper_bound TIMESTAMPTZ;
BEGIN
    FOR part IN
        SELECT c.relname
        FROM pg_inherits i
        JOIN pg_class c ON c.oid = i.inhrelid
        JOIN pg_class p ON p.oid = i.inhparent
        WHERE p.relname = 'audit_events'
          AND c.relname ~ '^audit_events_[0-9]{4}_[0-9]{2}$'
    LOOP
        -- Upper bound = first instant of the month AFTER the partition's month,
        -- derived from the deterministic partition name.
        upper_bound := date_trunc(
            'month',
            to_date(substring(part.relname FROM 'audit_events_([0-9]{4}_[0-9]{2})'), 'YYYY_MM')
        ) + INTERVAL '1 month';
        IF upper_bound <= cutoff THEN
            EXECUTE format('DROP TABLE %I', part.relname);
            dropped := dropped + 1;
        END IF;
    END LOOP;
    RETURN dropped;
END;
$_$;

CREATE OR REPLACE FUNCTION public.audit_events_secure_partition(target regclass) RETURNS void
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

CREATE OR REPLACE FUNCTION public.audit_events_secure_all_partitions() RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $$
DECLARE
    part regclass;
BEGIN
    FOR part IN
        SELECT inhrelid::regclass FROM pg_catalog.pg_inherits
         WHERE inhparent = 'public.audit_events'::regclass
         ORDER BY inhrelid
    LOOP
        PERFORM public.audit_events_secure_partition(part);
    END LOOP;
END;
$$;

DROP FUNCTION public.audit_events_provision_partitions(integer);
DROP FUNCTION public.audit_events_create_partitions(date[], timestamp with time zone);
DROP FUNCTION public.audit_events_secure_partition_within(regclass, timestamp with time zone);
DROP FUNCTION public.audit_partitions_due(timestamp with time zone);
DROP FUNCTION public.audit_partition_lock(regclass, timestamp with time zone);
DROP FUNCTION public.audit_partition_lock_budget(timestamp with time zone);
