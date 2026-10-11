-- Migration 33: audit retention and audit partition provisioning act on the
-- ranges PostgreSQL records, decide under lock, and wait ten seconds at most;
-- a retention window is a positive number of days.
--
-- A RETENTION WINDOW IS A POSITIVE NUMBER OF DAYS. data_retention_policies says
-- how many days each resource's records are kept. A window of zero days, or
-- fewer, deleted every record older than the run, and retention now refuses one
-- rather than read it as anything else. The table therefore refuses one too, and
-- this migration refuses to apply while a policy holds one: which window was
-- meant is the operator's to say, and a migration that refuses is recoverable
-- where a deletion is not.
--
-- RETENTION, audit_events_drop_partitions_before, runs on a schedule with nobody
-- watching, and a dropped partition cannot be recovered. What it may drop is held
-- by the rules below.
--
-- The bounds are the partition's own. A partition's name spells a month, but an
-- earlier release cut its bounds at the midnights of the session zone that
-- created it: created from New York, audit_events_2026_08 ends at 2026-09-01
-- 04:00 UTC, so a cutoff of 2026-09-01 00:00 UTC leaves it, and its last four
-- hours of events, alone. Each candidate's upper bound is read from
-- pg_get_expr(relpartbound) and parsed back, never derived from the name.
-- audit_events.created_at is timestamp with time zone, so the bound renders as an
-- instant, and the function renders and reads it under a DateStyle and TimeZone
-- it pins itself. ISO renders an instant with its numeric UTC offset, which reads
-- back as the same instant. Every other DateStyle renders it with the session
-- zone's abbreviation, and an abbreviation does not name one offset, so the
-- caller's settings would choose the instant the bound reads as. A partition
-- whose bounds are not a quoted FROM/TO pair -- DEFAULT, or a range open at
-- either end (MINVALUE, MAXVALUE) -- is never dropped: a cutoff rule cannot tell
-- what it holds. Only monthly-named partitions are candidates; the name selects
-- what retention may consider, and the bounds decide what it drops.
--
-- The cutoff is one the policy allows, by this database's clock. The function
-- reads the audit_events policy and refuses, with invalid_parameter_value
-- (22023), a cutoff later than the start of its transaction less the policy's
-- window, a missing one, and any when there is no policy. A caller's clock
-- therefore cannot move the window: a cutoff computed on a clock running hours
-- ahead is refused, not honoured by dropping a partition still inside the
-- window. Days are of 24 hours, counted in UTC.
--
-- The policy is the one in force when the drop commits. Once something is due
-- the function reads the policy again, with a row lock it holds until it
-- commits, and checks the cutoff against that: a change to the policy made
-- while the run waits for its locks waits in turn for the run to commit, and one
-- committed before is the policy the run keeps to. It does not wait for the
-- row: a change under way when it reads makes it refuse, with
-- lock_not_available (55P03), and the next run keeps to what the change leaves.
--
-- A partition being detached is not retention's. One whose DETACH ...
-- CONCURRENTLY has begun and not finished is leaving audit_events, and is never
-- dropped.
--
-- It decides under lock, from the catalog as it stands once the lock is held.
-- When something is due the function locks audit_events ACCESS EXCLUSIVE and
-- reads the catalog again: from then until the drop commits, no partition can
-- be created, attached, detached or given other bounds. That lock does not stop
-- a partition being renamed, and LOCK TABLE finds the table it locks by name:
-- were a partition renamed away while the function waited for it, and another
-- renamed to its name, a waiting LOCK TABLE would go on to lock that other one.
-- So the function locks each partition it chose by identity, under the name it
-- has at that moment and without waiting in LOCK TABLE, refusing unless the
-- lock it holds is on that very partition. Holding them all, it reads the
-- catalog a third time. It drops them only if exactly those are still due, and
-- otherwise drops nothing, with object_not_in_prerequisite_state (55000). Each
-- read sees what committed while the function waited only if it takes a
-- snapshot of its own, which a statement does at READ COMMITTED. At REPEATABLE
-- READ or SERIALIZABLE every statement reads the snapshot the transaction took
-- first, before the lock, in which a partition detached meanwhile is still
-- attached and due. The function therefore runs only at READ COMMITTED, and
-- refuses any other level with invalid_transaction_state (25000); the store's
-- retention call opens its transaction at READ COMMITTED, whatever the database
-- default. The lock order is the one inserters use (parent, then partition), so
-- it cannot deadlock with them, and a writer that arrives meanwhile waits for
-- the drop to commit.
--
-- It waits ten seconds at most, in all. lock_timeout bounds one wait, and the
-- function may wait more than once: for the policy's table, for audit_events,
-- then for each partition it drops. So it takes the policy's table and
-- audit_events each in a statement of its own, after setting lock_timeout to
-- what remains of ten seconds from its start, and a partition with
-- LOCK TABLE ... NOWAIT, tried again until those ten seconds are spent. Each
-- DROP runs under what remains too, which bounds its wait for the DEFAULT
-- partition, if there is one: dropping a range partition locks it as well. It
-- refuses with lock_not_available (55P03) once nothing remains. A run that
-- cannot have its locks in time drops nothing, and the next scheduled run tries
-- again, instead of holding audit writes behind it for as long as a reader
-- runs. When nothing is due the function takes no lock at all: a partition
-- comes due about once a month, and locking the table on every run would stall
-- audit writes behind any long reader for a drop that never happens. It reads
-- bounds without opening the partitions (pg_get_expr with no relation, which a
-- bound, holding no column reference, does not need), so that read waits for
-- nothing either.
--
-- PROVISIONING, audit_events_provision_partitions, which the store calls at
-- startup and on each retention tick, creates the months from the previous one
-- to months_ahead ahead, by this database's clock, and brings every partition's
-- row-level security in step with the parent's, all under one ten-second
-- budget. audit_events_ensure_partition and audit_events_secure_all_partitions,
-- which migrations and operators call, keep their signatures and get a budget of
-- their own. Creating a partition locks audit_events ACCESS EXCLUSIVE, the lock
-- retention takes, and it is taken the same way: only when a month lacks its
-- partition, in a statement of its own, within the budget, deciding again under
-- the lock what to create. Bringing a partition's row security in step locks
-- the partition only when it is out of step, by identity as retention does, and
-- decides again under that lock what to change. Bringing every partition in
-- step reads the list of partitions holding audit_events ACCESS SHARE, so none
-- it lists can be dropped before it reaches it.
--
-- A new partition's bounds are UTC midnights, whatever the zone of the session
-- that creates it, so its range no longer depends on who provisioned it. A
-- partition an earlier release cut at another zone's midnight still borders it,
-- and a range starting at UTC midnight would overlap one cut west of UTC, which
-- PostgreSQL refuses, or leave a gap after one cut east of it, where an insert
-- finds no partition. So a new partition starts where its predecessor ends, and
-- ends where its successor starts, when that neighbour's bound lies within a day
-- of the UTC midnight; every other partition stays out of its range.
--
-- The signatures, return values, owners and grants of the existing functions
-- are unchanged: CREATE OR REPLACE keeps the owner and the ACL. Their
-- search_path lists pg_catalog first and pg_temp last, like every other definer
-- function here.

DO $$
DECLARE
    invalid text;
BEGIN
    SELECT string_agg(format('%s (%s days)', resource_type, retention_days), ', ' ORDER BY resource_type)
      INTO invalid
      FROM public.data_retention_policies
     WHERE retention_days <= 0;
    IF invalid IS NOT NULL THEN
        RAISE EXCEPTION USING
            MESSAGE = format('data_retention_policies keeps records for a number of days that is not positive: %s', invalid),
            DETAIL = 'A window of zero days or fewer deleted every record older than the run, and retention now refuses one.',
            HINT = 'Set each to the number of days its records are kept, or delete the policy to keep them indefinitely, then apply this migration again.',
            ERRCODE = 'check_violation';
    END IF;
END
$$;

ALTER TABLE public.data_retention_policies
    ADD CONSTRAINT data_retention_policies_retention_days_positive CHECK (retention_days > 0);

-- audit_partition_lock_budget sets lock_timeout, until the calling maintenance
-- function returns, to what remains before deadline, and refuses once nothing
-- does. Every audit partition function calls it before each statement that may
-- wait for a lock, and each such statement waits for one lock at most, so the
-- waits of one call add up to its budget at most. A partition is locked by
-- audit_partition_lock, which never waits in LOCK TABLE and stops trying at the
-- same deadline. Owned by the migration principal and called only from the
-- functions below.
CREATE FUNCTION public.audit_partition_lock_budget(deadline timestamp with time zone) RETURNS void
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $$
DECLARE
    remaining_ms CONSTANT bigint := floor(extract(epoch FROM deadline - clock_timestamp()) * 1000);
BEGIN
    IF remaining_ms < 1 THEN
        RAISE EXCEPTION 'audit partition maintenance has waited for its locks as long as it may'
            USING ERRCODE = 'lock_not_available';
    END IF;
    PERFORM set_config('lock_timeout', remaining_ms::text, true);
END;
$$;

REVOKE ALL ON FUNCTION public.audit_partition_lock_budget(timestamp with time zone) FROM PUBLIC;

-- audit_partition_lock(target, deadline) locks target ACCESS EXCLUSIVE before
-- deadline, and refuses unless the lock it then holds is on target itself.
-- LOCK TABLE finds the table it locks by name, and a name can pass to another
-- table while a lock is awaited: were target renamed away and another table
-- renamed to its name meanwhile, a waiting LOCK TABLE would go on to that other
-- table and wait for it too, with the same lock_timeout, past the deadline. So
-- this never waits in LOCK TABLE. It tries with NOWAIT, under the name target
-- has at that moment, and tries again every 50 milliseconds until deadline, so a
-- partition renamed meanwhile is still the one it locks. Trying again gives up
-- the place a waiting lock keeps in the queue: retention tries holding
-- audit_events ACCESS EXCLUSIVE, which every query through the parent waits for,
-- so only a session using the partition directly can hold it off. Once target
-- is held it cannot be renamed, so each statement after this one that names it
-- reaches it. Owned by the migration principal and called only from the
-- functions below.
CREATE FUNCTION public.audit_partition_lock(target regclass, deadline timestamp with time zone) RETURNS void
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $$
DECLARE
    named text;
BEGIN
    LOOP
        SELECT format('%I.%I', n.nspname, c.relname) INTO named
          FROM pg_catalog.pg_class c
          JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
         WHERE c.oid = target;
        EXIT WHEN named IS NULL;
        BEGIN
            EXECUTE format('LOCK TABLE %s IN ACCESS EXCLUSIVE MODE NOWAIT', named);
            EXIT;
        EXCEPTION WHEN lock_not_available OR undefined_table THEN
            -- Held by another session, or renamed since it was looked up.
            NULL;
        END;
        IF clock_timestamp() >= deadline THEN
            RAISE EXCEPTION 'audit partition maintenance has waited for its locks as long as it may'
                USING ERRCODE = 'lock_not_available';
        END IF;
        PERFORM pg_sleep(least(0.05, extract(epoch FROM deadline - clock_timestamp())));
    END LOOP;
    IF NOT EXISTS (
        SELECT 1 FROM pg_catalog.pg_locks l
         WHERE l.locktype = 'relation'
           AND l.database = (SELECT d.oid FROM pg_catalog.pg_database d WHERE d.datname = current_database())
           AND l.relation = target
           AND l.pid = pg_backend_pid()
           AND l.mode = 'AccessExclusiveLock'
           AND l.granted
    ) THEN
        RAISE EXCEPTION 'audit partition % was dropped, or its name passed to another table, while maintenance waited for its lock', coalesce(named, target::text)
            USING ERRCODE = 'object_not_in_prerequisite_state',
                  HINT = 'Nothing was changed. The next run decides again from the catalog as it then stands.';
    END IF;
END;
$$;

REVOKE ALL ON FUNCTION public.audit_partition_lock(regclass, timestamp with time zone) FROM PUBLIC;

-- audit_partitions_due(cutoff) is what retention drops: the partitions of
-- audit_events with a monthly name, not being detached, whose recorded range
-- ends at or before cutoff, in name order, or NULL when there are none. It reads
-- their bounds without opening them, so it waits for no lock. Owned by the
-- migration principal and called only from audit_events_drop_partitions_before.
CREATE FUNCTION public.audit_partitions_due(cutoff timestamp with time zone) RETURNS regclass[]
    LANGUAGE sql
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    SET datestyle TO 'ISO, MDY'
    SET timezone TO 'UTC'
    AS $$
    SELECT array_agg(candidate.part ORDER BY candidate.name)
      FROM (
          SELECT c.oid::regclass AS part,
                 c.relname AS name,
                 -- NULL when the bounds are not a quoted FROM/TO pair, which
                 -- the comparison below then leaves out.
                 (regexp_match(pg_get_expr(c.relpartbound, 0),
                               '^FOR VALUES FROM \(''([^'']+)''\) TO \(''([^'']+)''\)$'))[2]::timestamp with time zone AS upper_bound
            FROM pg_inherits i
            JOIN pg_class c ON c.oid = i.inhrelid
           WHERE i.inhparent = 'public.audit_events'::regclass
             AND NOT i.inhdetachpending
             AND c.relkind = 'r'
             AND c.relnamespace = 'public'::regnamespace
             AND c.relname ~ '^audit_events_[0-9]{4}_[0-9]{2}$'
      ) candidate
     WHERE candidate.upper_bound <= cutoff
$$;

REVOKE ALL ON FUNCTION public.audit_partitions_due(timestamp with time zone) FROM PUBLIC;

CREATE OR REPLACE FUNCTION public.audit_events_drop_partitions_before(cutoff timestamp with time zone) RETURNS integer
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    SET lock_timeout TO '10s'
    SET datestyle TO 'ISO, MDY'
    SET timezone TO 'UTC'
    AS $$
DECLARE
    deadline     CONSTANT timestamp with time zone := clock_timestamp() + interval '10 seconds';
    window_days  integer;
    latest       timestamp with time zone;
    due          regclass[];
    part         regclass;
    dropped      integer := 0;
BEGIN
    FOR pass IN 1..2 LOOP
        -- Pass 1 reads the policy with no lock, to refuse a cutoff it does not
        -- allow before taking any, and returns when nothing is due. Pass 2,
        -- once something is, reads it again with a row lock held until the
        -- drop commits, so the cutoff is held to the policy in force then.
        IF pass = 1 THEN
            SELECT p.retention_days INTO window_days
              FROM public.data_retention_policies p
             WHERE p.resource_type = 'audit_events';
        ELSE
            PERFORM public.audit_partition_lock_budget(deadline);
            SELECT p.retention_days INTO window_days
              FROM public.data_retention_policies p
             WHERE p.resource_type = 'audit_events'
               FOR SHARE NOWAIT;
        END IF;
        IF window_days IS NULL THEN
            RAISE EXCEPTION 'audit_events has no retention policy, so no partition is due'
                USING ERRCODE = 'invalid_parameter_value';
        END IF;
        -- A window reaching back past the earliest instant a timestamp holds,
        -- 24 November 4714 BC, retains every partition.
        latest := CASE WHEN window_days < now()::date - '4714-11-24 BC'::date
                       THEN now() - make_interval(days => window_days)
                       ELSE '-infinity'::timestamp with time zone END;
        IF cutoff IS NULL THEN
            RAISE EXCEPTION 'audit retention needs a cutoff'
                USING ERRCODE = 'invalid_parameter_value';
        END IF;
        IF cutoff > latest THEN
            RAISE EXCEPTION 'audit retention cutoff % is later than the audit_events policy of % days allows', cutoff, window_days
                USING ERRCODE = 'invalid_parameter_value',
                      DETAIL = format('The latest cutoff the policy allows is %s.', latest);
        END IF;
        IF pass = 1 THEN
            -- PostgreSQL runs READ UNCOMMITTED as READ COMMITTED.
            IF current_setting('transaction_isolation') NOT IN ('read committed', 'read uncommitted') THEN
                RAISE EXCEPTION 'audit retention runs only at READ COMMITTED, not %', upper(current_setting('transaction_isolation'))
                    USING ERRCODE = 'invalid_transaction_state',
                          HINT = 'Run it in a transaction begun with ISOLATION LEVEL READ COMMITTED.';
            END IF;
            IF public.audit_partitions_due(cutoff) IS NULL THEN
                RETURN 0;
            END IF;
        END IF;
    END LOOP;

    -- With audit_events held, no partition can be created, attached, detached
    -- or given other bounds until the drop commits.
    PERFORM public.audit_partition_lock_budget(deadline);
    LOCK TABLE ONLY public.audit_events IN ACCESS EXCLUSIVE MODE;
    due := public.audit_partitions_due(cutoff);
    IF due IS NULL THEN
        RETURN 0;
    END IF;

    -- A partition can still be renamed until it is held itself. Each is locked
    -- by identity; with all of them held, the choice is made once more, and
    -- must come out the same.
    FOREACH part IN ARRAY due LOOP
        PERFORM public.audit_partition_lock(part, deadline);
    END LOOP;
    IF public.audit_partitions_due(cutoff) IS DISTINCT FROM due THEN
        RAISE EXCEPTION 'the audit partitions due changed while retention waited for their locks, so it dropped none'
            USING ERRCODE = 'object_not_in_prerequisite_state',
                  HINT = 'The next run decides again from the catalog as it then stands.';
    END IF;
    FOREACH part IN ARRAY due LOOP
        -- Dropping a range partition also locks the DEFAULT partition, if
        -- there is one, by identity, since its constraint changes: the first
        -- DROP waits for it no longer than what remains.
        PERFORM public.audit_partition_lock_budget(deadline);
        EXECUTE format('DROP TABLE %s', part);
        dropped := dropped + 1;
    END LOOP;
    RETURN dropped;
END;
$$;

-- audit_events_secure_partition_within(target, deadline) brings one partition's
-- row security in step with the parent's, as migration 11's
-- audit_events_secure_partition did, and waits for its locks only until
-- deadline. A policy's expressions render only with their table open, which
-- waits for a session holding the table ACCESS EXCLUSIVE, so the parent's and
-- the partition's are read in statements of their own. A partition already in
-- step takes no lock beyond those reads; one out of step is locked by identity
-- and read again under that lock, which decides what to change.
CREATE FUNCTION public.audit_events_secure_partition_within(target regclass, deadline timestamp with time zone) RETURNS void
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $$
DECLARE
    parent     CONSTANT regclass := 'public.audit_events'::regclass;
    pol        record;
    role_list  text;
    enable_rls boolean;
    force_rls  boolean;
    wanted     text[];
    carried    text[];
BEGIN
    FOR pass IN 1..2 LOOP
        -- Pass 1 reads whether the partition is in step without locking it.
        -- Pass 2 holds it, and reads again what to change.
        IF pass = 2 THEN
            PERFORM public.audit_partition_lock(target, deadline);
        END IF;
        IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_inherits WHERE inhrelid = target AND inhparent = parent) THEN
            RAISE EXCEPTION '% is not a partition of %', target, parent;
        END IF;
        SELECT NOT c.relrowsecurity, NOT c.relforcerowsecurity INTO enable_rls, force_rls
          FROM pg_catalog.pg_class c WHERE c.oid = target;

        PERFORM public.audit_partition_lock_budget(deadline);
        SELECT array_agg(concat_ws(chr(31), polname, polpermissive, polcmd, polroles::text,
                                   coalesce(pg_get_expr(polqual, polrelid), ''), coalesce(pg_get_expr(polwithcheck, polrelid), ''))
                         ORDER BY polname)
          INTO wanted FROM pg_catalog.pg_policy WHERE polrelid = parent;
        PERFORM public.audit_partition_lock_budget(deadline);
        SELECT array_agg(concat_ws(chr(31), polname, polpermissive, polcmd, polroles::text,
                                   coalesce(pg_get_expr(polqual, polrelid), ''), coalesce(pg_get_expr(polwithcheck, polrelid), ''))
                         ORDER BY polname)
          INTO carried FROM pg_catalog.pg_policy WHERE polrelid = target;

        IF NOT enable_rls AND NOT force_rls AND wanted IS NOT DISTINCT FROM carried THEN
            RETURN;
        END IF;
    END LOOP;

    IF enable_rls THEN
        EXECUTE format('ALTER TABLE %s ENABLE ROW LEVEL SECURITY', target);
    END IF;
    IF force_rls THEN
        EXECUTE format('ALTER TABLE %s FORCE ROW LEVEL SECURITY', target);
    END IF;
    IF wanted IS NOT DISTINCT FROM carried THEN
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

REVOKE ALL ON FUNCTION public.audit_events_secure_partition_within(regclass, timestamp with time zone) FROM PUBLIC;

-- The one-argument form, which migrations 11 and 13 called, keeps its signature
-- and gets a budget of its own.
CREATE OR REPLACE FUNCTION public.audit_events_secure_partition(target regclass) RETURNS void
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    SET lock_timeout TO '10s'
    AS $$
BEGIN
    PERFORM public.audit_events_secure_partition_within(target, clock_timestamp() + interval '10 seconds');
END;
$$;

-- audit_events_create_partitions creates the partition of each month in months
-- that has none, with UTC bounds that meet its neighbours, and secures each one
-- in the transaction that creates it, waiting for its locks only until
-- deadline. Owned by the migration principal and called only from the
-- provisioning functions below.
CREATE FUNCTION public.audit_events_create_partitions(months date[], deadline timestamp with time zone) RETURNS void
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    SET datestyle TO 'ISO, MDY'
    SET timezone TO 'UTC'
    AS $$
DECLARE
    parent    CONSTANT regclass := 'public.audit_events'::regclass;
    bounds    CONSTANT text := '^FOR VALUES FROM \(''([^'']+)''\) TO \(''([^'']+)''\)$';
    month     date;
    part_name text;
    start_utc timestamp with time zone;
    end_utc   timestamp with time zone;
    lo        timestamp with time zone;
    hi        timestamp with time zone;
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM unnest(months) AS m
         WHERE to_regclass('public.' || quote_ident('audit_events_' || to_char(m, 'YYYY_MM'))) IS NULL
    ) THEN
        RETURN;
    END IF;

    -- Under the lock no partition can be created, attached or detached, so what
    -- is read next stays true until the transaction ends.
    PERFORM public.audit_partition_lock_budget(deadline);
    LOCK TABLE ONLY public.audit_events IN ACCESS EXCLUSIVE MODE;

    FOREACH month IN ARRAY months LOOP
        part_name := 'audit_events_' || to_char(month, 'YYYY_MM');
        CONTINUE WHEN to_regclass('public.' || quote_ident(part_name)) IS NOT NULL;
        start_utc := date_trunc('month', month::timestamp) AT TIME ZONE 'UTC';
        end_utc := (date_trunc('month', month::timestamp) + interval '1 month') AT TIME ZONE 'UTC';

        WITH attached AS (
            SELECT range[1]::timestamp with time zone AS lower_bound,
                   range[2]::timestamp with time zone AS upper_bound
              FROM pg_catalog.pg_inherits i
              JOIN pg_catalog.pg_class c ON c.oid = i.inhrelid
             CROSS JOIN LATERAL regexp_match(pg_catalog.pg_get_expr(c.relpartbound, 0), bounds) AS range
             WHERE i.inhparent = parent
        )
        SELECT coalesce((SELECT max(upper_bound) FROM attached
                          WHERE lower_bound < start_utc
                            AND upper_bound > start_utc - interval '1 day'
                            AND upper_bound < start_utc + interval '1 day'), start_utc),
               coalesce((SELECT min(lower_bound) FROM attached
                          WHERE upper_bound > end_utc
                            AND lower_bound > end_utc - interval '1 day'
                            AND lower_bound < end_utc + interval '1 day'), end_utc)
          INTO lo, hi;
        -- Its neighbours already hold the whole month.
        CONTINUE WHEN lo >= hi;

        EXECUTE format('CREATE TABLE public.%I PARTITION OF public.audit_events FOR VALUES FROM (%L) TO (%L)',
                       part_name, lo, hi);
        PERFORM public.audit_events_secure_partition_within(to_regclass('public.' || quote_ident(part_name)), deadline);
    END LOOP;
END;
$$;

REVOKE ALL ON FUNCTION public.audit_events_create_partitions(date[], timestamp with time zone) FROM PUBLIC;

-- A partition is secured in the same transaction that creates it, and an
-- existing one is brought back in step whenever it is ensured, so none is ever
-- readable by name without the parent's policies.
CREATE OR REPLACE FUNCTION public.audit_events_ensure_partition(month date) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    SET lock_timeout TO '10s'
    AS $$
DECLARE
    deadline CONSTANT timestamp with time zone := clock_timestamp() + interval '10 seconds';
    part     regclass;
BEGIN
    PERFORM public.audit_events_create_partitions(ARRAY[month], deadline);
    part := to_regclass('public.' || quote_ident('audit_events_' || to_char(month, 'YYYY_MM')));
    IF part IS NOT NULL THEN
        PERFORM public.audit_events_secure_partition_within(part, deadline);
    END IF;
END;
$$;

CREATE OR REPLACE FUNCTION public.audit_events_secure_all_partitions() RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    SET lock_timeout TO '10s'
    AS $$
DECLARE
    deadline CONSTANT timestamp with time zone := clock_timestamp() + interval '10 seconds';
    part     regclass;
BEGIN
    -- Holding audit_events, no partition listed below can be dropped or
    -- detached before it is reached.
    PERFORM public.audit_partition_lock_budget(deadline);
    LOCK TABLE ONLY public.audit_events IN ACCESS SHARE MODE;
    FOR part IN
        SELECT inhrelid::regclass FROM pg_catalog.pg_inherits
         WHERE inhparent = 'public.audit_events'::regclass
         ORDER BY inhrelid
    LOOP
        PERFORM public.audit_events_secure_partition_within(part, deadline);
    END LOOP;
END;
$$;

-- audit_events_provision_partitions is the store's one provisioning call: the
-- months from the previous one to months_ahead ahead of this one, by this
-- database's clock in UTC, then every partition's row security, all within ten
-- seconds of waiting. Execute is the control plane's alone, like the functions
-- it replaces in that call.
CREATE FUNCTION public.audit_events_provision_partitions(months_ahead integer) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    SET lock_timeout TO '10s'
    AS $$
DECLARE
    deadline   CONSTANT timestamp with time zone := clock_timestamp() + interval '10 seconds';
    this_month CONSTANT date := date_trunc('month', now() AT TIME ZONE 'UTC')::date;
    part       regclass;
BEGIN
    IF months_ahead IS NULL OR months_ahead < 0 OR months_ahead > 24 THEN
        RAISE EXCEPTION 'audit partitions are provisioned from 0 to 24 months ahead, not %', coalesce(months_ahead::text, 'NULL')
            USING ERRCODE = 'invalid_parameter_value';
    END IF;
    PERFORM public.audit_events_create_partitions(
        ARRAY(SELECT (this_month + make_interval(months => m))::date FROM generate_series(-1, months_ahead) AS m ORDER BY m),
        deadline);
    -- Holding audit_events, no partition listed below can be dropped or
    -- detached before it is reached.
    PERFORM public.audit_partition_lock_budget(deadline);
    LOCK TABLE ONLY public.audit_events IN ACCESS SHARE MODE;
    FOR part IN
        SELECT inhrelid::regclass FROM pg_catalog.pg_inherits
         WHERE inhparent = 'public.audit_events'::regclass
         ORDER BY inhrelid
    LOOP
        PERFORM public.audit_events_secure_partition_within(part, deadline);
    END LOOP;
END;
$$;

REVOKE ALL ON FUNCTION public.audit_events_provision_partitions(integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.audit_events_provision_partitions(integer) TO app_control_plane;
