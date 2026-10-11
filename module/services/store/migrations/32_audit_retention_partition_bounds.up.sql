-- Migration 32: audit retention drops a partition only when the range
-- PostgreSQL records for it ends by the cutoff, and decides which under lock.
--
-- audit_events_drop_partitions_before is how audit retention runs, on a
-- schedule with nobody watching, and a dropped partition cannot be recovered.
-- What it may drop is held by five rules.
--
-- The bounds are the partition's own. A partition's name spells a month, but
-- its bounds are the midnights of the session zone that created it: created
-- from New York, audit_events_2026_08 ends at 2026-09-01 04:00 UTC, so a cutoff
-- of 2026-09-01 00:00 UTC leaves it, and its last four hours of events, alone.
-- Each candidate's upper bound is read from pg_get_expr(relpartbound) and parsed
-- back, never derived from the name. audit_events.created_at is timestamp with
-- time zone, so the bound renders as an instant, and the function renders and
-- reads it under a DateStyle and TimeZone it pins itself. ISO renders an instant
-- with its numeric UTC offset, which reads back as the same instant. Every other
-- DateStyle renders it with the session zone's abbreviation, and an abbreviation
-- does not name one offset, so the caller's settings would choose the instant
-- the bound reads as. A partition whose bounds are not a quoted FROM/TO pair --
-- DEFAULT, or a range open at either end (MINVALUE, MAXVALUE) -- is never
-- dropped: a cutoff rule cannot tell what it holds. Only monthly-named
-- partitions are candidates, as before; the name selects what retention may
-- consider, and the bounds decide what it drops.
--
-- The cutoff is in the past. A cutoff later than the start of the transaction
-- reaches the partition holding the present and the ones provisioned ahead of
-- it; the function refuses one, and a missing one, with invalid_parameter_value
-- (22023), and drops nothing.
--
-- A partition being detached is not retention's. One whose DETACH ...
-- CONCURRENTLY has begun and not finished is leaving audit_events, and is never
-- dropped.
--
-- It decides under lock, from the catalog as it stands once the lock is held.
-- When something is due the function locks audit_events ACCESS EXCLUSIVE, which
-- also locks every partition, then each partition it is about to drop, and reads
-- the catalog again before it drops any: from then until the drop commits, no
-- partition can be attached or detached. That read sees what committed while
-- the function waited for its lock only if it takes a snapshot of its own, which
-- a statement does at READ COMMITTED. At REPEATABLE READ or SERIALIZABLE every
-- statement reads the snapshot the transaction took first, before the lock, in
-- which a partition detached meanwhile is still attached and due. The function
-- therefore runs only at READ COMMITTED, and refuses any other level with
-- invalid_transaction_state (25000); the store's retention call opens its
-- transaction at READ COMMITTED, whatever the database default. The lock order
-- is the one inserters use (parent, then partition), so it cannot deadlock with
-- them, and a writer that arrives meanwhile waits for the drop to commit.
--
-- It waits ten seconds at most. lock_timeout is 10s: a writer that does not
-- finish makes the run fail with lock_not_available (55P03) and drop nothing,
-- and the next scheduled run tries again, instead of holding audit writes behind
-- it for as long as the writer runs. When nothing is due the function takes no
-- lock at all: a partition comes due about once a month, and locking the table
-- on every run would stall audit writes behind any long reader for a drop that
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
    SET datestyle TO 'ISO, MDY'
    SET timezone TO 'UTC'
    AS $$
DECLARE
    parent    CONSTANT regclass := 'public.audit_events'::regclass;
    bounds    CONSTANT text := '^FOR VALUES FROM \(''([^'']+)''\) TO \(''([^'']+)''\)$';
    due       text[];
    part_name text;
    dropped   integer := 0;
BEGIN
    IF cutoff IS NULL OR cutoff > now() THEN
        RAISE EXCEPTION 'audit retention cutoff % is not in the past', coalesce(cutoff::text, 'NULL')
            USING ERRCODE = 'invalid_parameter_value';
    END IF;
    -- PostgreSQL runs READ UNCOMMITTED as READ COMMITTED.
    IF current_setting('transaction_isolation') NOT IN ('read committed', 'read uncommitted') THEN
        RAISE EXCEPTION 'audit retention runs only at READ COMMITTED, not %', upper(current_setting('transaction_isolation'))
            USING ERRCODE = 'invalid_transaction_state',
                  HINT = 'Run it in a transaction begun with ISOLATION LEVEL READ COMMITTED.';
    END IF;

    FOR pass IN 1..2 LOOP
        -- Pass 1 reads the catalog without a lock, to learn whether anything is
        -- due. Pass 2 reads it again with audit_events held ACCESS EXCLUSIVE, in
        -- a snapshot taken after the lock, and decides.
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
                 AND NOT i.inhdetachpending
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
