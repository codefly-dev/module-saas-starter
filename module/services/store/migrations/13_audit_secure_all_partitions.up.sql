-- Migration 13: audit_events_ensure_partition brings a partition in step with its
-- parent's row-level security only for the months it is asked to ensure, and
-- accounts ensures the previous month through three months ahead while retention
-- keeps about a year of partitions. A later migration that changes the parent's
-- policies would therefore leave every older partition on the old set until it
-- aged out, and a partition queried by name is checked against its own policies,
-- never the parent's.
--
-- audit_events_secure_all_partitions() runs audit_events_secure_partition over
-- every partition of audit_events. A partition already in step costs a catalog
-- read and takes no lock, so accounts calls it on every ensure, at startup and on
-- each retention tick. Like audit_events_ensure_partition it is SECURITY DEFINER,
-- owned by the migration principal that owns the partitions, lists pg_temp last
-- in its search_path so a caller's temporary relations cannot shadow the catalog,
-- and app_control_plane, the role those paths run as, is the only runtime role
-- that may execute it.
CREATE FUNCTION public.audit_events_secure_all_partitions() RETURNS void
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

REVOKE ALL ON FUNCTION public.audit_events_secure_all_partitions() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.audit_events_secure_all_partitions() TO app_control_plane;
