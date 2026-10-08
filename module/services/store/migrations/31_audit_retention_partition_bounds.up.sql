-- Migration 31: audit retention drops the partitions PostgreSQL says end by the
-- cutoff, under lock, instead of the months their names spell.
--
-- audit_events_drop_partitions_before is how audit retention runs on a schedule,
-- with nobody watching. It had two defects:
--
--   * It chose partitions by the month in their NAME and cast that month to a
--     timestamp in the calling session's time zone. A partition's real bounds
--     are the ones its creator's zone gave it: created from a New York session,
--     audit_events_2026_08 ends at 2026-09-01 00:00-04, which is 04:00 UTC.
--     Retention run from a UTC session with a cutoff of 2026-09-01 00:00 UTC
--     computed the same month's end as 00:00 UTC, judged the partition older than
--     the cutoff, and dropped its last four hours of events, which are newer.
--   * It locked nothing and set no lock_timeout. A DROP waits for a transaction
--     inserting into the partition and then takes that transaction's row with the
--     table, unseen, and the wait has no bound, so a stuck writer hung retention.
--
-- It now decides from what PostgreSQL holds. Each candidate's upper bound is read
-- from pg_get_expr(relpartbound) and parsed back, never derived from the name.
-- The bound is rendered in this session's zone with its UTC offset, and read back
-- in the same session, so the instant is the partition's own whatever zone made
-- it; audit_events.created_at is timestamp with time zone, so the rendered bound
-- always carries one. A partition whose bounds are not a quoted FROM/TO pair --
-- DEFAULT, or a range open at either end (MINVALUE, MAXVALUE) -- is never
-- dropped: a cutoff rule cannot tell what it holds. Only monthly-named
-- partitions are candidates, as before; the name selects what retention may
-- consider, and the bounds decide what it drops.
--
-- When something is due the function locks audit_events ACCESS EXCLUSIVE, which
-- also locks every partition, then each partition it is about to drop, and only
-- then drops. Every row in a partition whose range ends by the cutoff is older
-- than the cutoff, so retention may drop it; what the locks give is that no insert
-- is in flight into a partition as it goes, and that the partitions dropped are
-- the ones decided on. The lock order is the one inserters use (parent, then
-- partition), so it cannot deadlock with them, and a writer that arrives
-- meanwhile waits for the drop to commit. The decision is taken again once the
-- lock is held: bounds read before it are only a hint that something is due.
-- lock_timeout is 10s, so a writer that does not finish makes retention fail with
-- lock_not_available (55P03) and drop nothing, and the next scheduled run tries
-- again, instead of blocking for as long as the writer does.
--
-- When nothing is due the function takes no lock at all. Retention runs on a
-- schedule and a partition comes due about once a month, so locking the table on
-- every run would stall audit writes behind any long reader for a drop that
-- never happens.
--
-- The signature, the return value (the number of partitions dropped), the owner
-- and the grants are unchanged: CREATE OR REPLACE keeps the owner and the ACL,
-- so execute stays with app_control_plane alone. The search_path now lists
-- pg_catalog first and pg_temp last, like every other definer function here.
CREATE OR REPLACE FUNCTION public.audit_events_drop_partitions_before(cutoff timestamp with time zone) RETURNS integer
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    SET lock_timeout TO '10s'
    AS $$
DECLARE
    parent    CONSTANT regclass := 'public.audit_events'::regclass;
    bounds    CONSTANT text := '^FOR VALUES FROM \(''([^'']+)''\) TO \(''([^'']+)''\)$';
    due       text[];
    part_name text;
    dropped   integer := 0;
BEGIN
    FOR pass IN 1..2 LOOP
        -- Pass 1 reads the catalog without a lock, to learn whether anything is
        -- due. Pass 2 reads it again with audit_events held ACCESS EXCLUSIVE, so
        -- that no partition is attached or detached between the read and the
        -- drop, and decides.
        IF pass = 2 THEN
            LOCK TABLE public.audit_events IN ACCESS EXCLUSIVE MODE;
        END IF;

        SELECT array_agg(candidate.name ORDER BY candidate.name) INTO due
          FROM (
              SELECT c.relname::text AS name,
                     -- NULL when the bounds do not match the pattern, which the
                     -- comparison below then leaves out.
                     (regexp_match(pg_get_expr(c.relpartbound, c.oid), bounds))[2]::timestamp with time zone AS upper_bound
                FROM pg_inherits i
                JOIN pg_class c ON c.oid = i.inhrelid
               WHERE i.inhparent = parent
                 AND c.relkind = 'r'
                 AND c.relnamespace = 'public'::regnamespace
                 AND c.relname ~ '^audit_events_[0-9]{4}_[0-9]{2}$'
          ) candidate
         WHERE candidate.upper_bound <= cutoff;

        IF due IS NULL THEN
            RETURN 0;
        END IF;
    END LOOP;

    -- The parent is locked and, with it, every partition; naming each one to be
    -- dropped keeps that true should the parent's lock ever stop recursing.
    FOREACH part_name IN ARRAY due LOOP
        EXECUTE format('LOCK TABLE public.%I IN ACCESS EXCLUSIVE MODE', part_name);
    END LOOP;
    FOREACH part_name IN ARRAY due LOOP
        EXECUTE format('DROP TABLE public.%I', part_name);
        dropped := dropped + 1;
    END LOOP;
    RETURN dropped;
END;
$$;
