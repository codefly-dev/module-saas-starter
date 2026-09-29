-- Reverts migration 11: restores the baseline audit_events_ensure_partition and takes
-- the partitions' own row-level security away again, which reopens the read
-- this migration closed: a partition queried by name is then checked against no
-- policy at all. The one difference from the baseline is kept on purpose: the
-- function still lists pg_temp last in its search_path and names its relations
-- by schema, so rolling back does not let a caller's temporary table shadow the
-- parent it creates partitions of.
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
END;
$$;

DO $partitions$
DECLARE
    part regclass;
    pol  record;
BEGIN
    FOR part IN SELECT inhrelid::regclass FROM pg_catalog.pg_inherits WHERE inhparent = 'public.audit_events'::regclass LOOP
        FOR pol IN SELECT polname FROM pg_catalog.pg_policy WHERE polrelid = part LOOP
            EXECUTE format('DROP POLICY %I ON %s', pol.polname, part);
        END LOOP;
        EXECUTE format('ALTER TABLE %s NO FORCE ROW LEVEL SECURITY', part);
        EXECUTE format('ALTER TABLE %s DISABLE ROW LEVEL SECURITY', part);
    END LOOP;
END
$partitions$;

DROP FUNCTION public.audit_events_secure_partition(regclass);
