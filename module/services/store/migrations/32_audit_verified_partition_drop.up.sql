-- Migration 32: drop the audit_events partitions a history copy verified, and
-- exactly those, atomically with a last check of what they hold.
--
-- The one-time history copy of a deployment switching to a warehouse store of
-- record (ADR 0009, item 7) copies audit_events into the warehouse, verifies the
-- copy, and then retires the copied monthly partitions. It used to do that
-- through audit_events_drop_partitions_before, which has two properties a
-- verified retirement cannot rely on:
--
--   * It chooses partitions by the month in their NAME, read in the calling
--     session's time zone. The copy chooses the partitions it verified by the
--     bounds PostgreSQL holds for them. A partition created from a session in
--     another zone has bounds on that zone's midnights, so the two choices
--     differ: a drop session in UTC asked to retire everything before 1 September
--     also dropped audit_events_2026_08, whose last four hours (when created
--     from New York) are September's events, which no one had verified.
--   * It drops what it finds. A row committed into a partition between the
--     copy's last recount and the DROP was destroyed unseen: nothing locked the
--     partition, and a DROP waits for an inserting transaction and then takes its
--     row with the table.
--
-- audit_events_drop_verified_partitions takes the EXPLICIT list of partitions
-- the copy verified, with the number of rows each held when verified, and in one
-- transaction:
--
--   1. locks audit_events and then each named partition ACCESS EXCLUSIVE, in a
--      fixed order, so no insert can route into a partition it is about to
--      count and drop, and an insert already in flight is waited for. The lock
--      wait is bounded; a writer that does not finish is a refusal, not a hang;
--   2. confirms from pg_catalog, after the lock, that each name is a monthly
--      partition of audit_events with a timestamp range whose UPPER bound is at
--      or before the cutoff;
--   3. recounts each partition and compares the count with the one verified;
--   4. only then drops exactly the named partitions, and returns their names.
--
-- Any mismatch raises SQLSTATE AH001 and the transaction rolls back with nothing
-- dropped. The caller compares the returned names with the verified set before
-- it commits.
--
-- The recount reads the partition by name as its owner. Partitions FORCE
-- row-level security, which holds an owner without BYPASSRLS (the migration
-- principal) to the policies: bound to no organization it would count none of the
-- rows. The function therefore lifts FORCE on each partition for the duration of
-- its own transaction before counting. The partition is held ACCESS EXCLUSIVE, so
-- no other session sees the change, and the transaction either drops the
-- partition or rolls the change back with everything else.
--
-- Owned by the migration principal, which owns the partitions. SECURITY DEFINER
-- with pg_temp last in its search_path, like every definer function in the
-- store. app_control_plane, the role retention and the history copy run as, is
-- the only runtime role that may execute it; it already holds the authority to
-- drop partitions through audit_events_drop_partitions_before, which stays in
-- place for retention and which the history copy no longer calls.
CREATE FUNCTION public.audit_events_drop_verified_partitions(
    cutoff        timestamp with time zone,
    names         text[],
    expected_rows bigint[]
) RETURNS text[]
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    SET lock_timeout TO '10s'
    AS $$
DECLARE
    parent      CONSTANT regclass := 'public.audit_events'::regclass;
    part_name   text;
    wanted      bigint;
    part        regclass;
    bound       text;
    bounds      text[];
    upper_bound timestamp with time zone;
    actual      bigint;
    dropped     text[] := ARRAY[]::text[];
BEGIN
    IF cutoff IS NULL OR names IS NULL OR expected_rows IS NULL
       OR array_ndims(names) IS DISTINCT FROM 1 OR array_ndims(expected_rows) IS DISTINCT FROM 1
       OR cardinality(names) = 0 OR cardinality(names) <> cardinality(expected_rows) THEN
        RAISE EXCEPTION 'audit history drop refused: a cutoff and one expected row count per partition name are required'
            USING ERRCODE = 'AH001';
    END IF;
    IF array_position(names, NULL) IS NOT NULL OR array_position(expected_rows, NULL) IS NOT NULL
       OR (SELECT count(DISTINCT n) FROM unnest(names) AS n) <> cardinality(names)
       OR (SELECT min(e) FROM unnest(expected_rows) AS e) < 0 THEN
        RAISE EXCEPTION 'audit history drop refused: partition names must be non-null and distinct, and every expected row count non-negative'
            USING ERRCODE = 'AH001';
    END IF;

    -- Parent first, then the partitions by name: inserters take the parent
    -- before a partition, so this order cannot deadlock with them.
    LOCK TABLE public.audit_events IN ACCESS EXCLUSIVE MODE;

    FOR part_name, wanted IN
        SELECT n, e FROM unnest(names, expected_rows) AS t(n, e) ORDER BY n
    LOOP
        IF part_name !~ '^audit_events_[0-9]{4}_[0-9]{2}$' THEN
            RAISE EXCEPTION 'audit history drop refused: % is not a monthly audit_events partition name', part_name
                USING ERRCODE = 'AH001';
        END IF;
        part := pg_catalog.to_regclass(format('public.%I', part_name));
        IF part IS NULL THEN
            RAISE EXCEPTION 'audit history drop refused: partition % does not exist', part_name
                USING ERRCODE = 'AH001';
        END IF;
        EXECUTE format('LOCK TABLE public.%I IN ACCESS EXCLUSIVE MODE', part_name);

        -- Read the bounds after the lock: PostgreSQL renders them in this
        -- session's zone, with the offset, and the cast below reads them back in
        -- the same one, so the instant is the partition's own.
        SELECT pg_catalog.pg_get_expr(c.relpartbound, c.oid) INTO bound
          FROM pg_catalog.pg_inherits i
          JOIN pg_catalog.pg_class c ON c.oid = i.inhrelid
         WHERE i.inhparent = parent AND c.oid = part AND c.relkind = 'r';
        IF NOT FOUND THEN
            RAISE EXCEPTION 'audit history drop refused: % is not attached to audit_events', part_name
                USING ERRCODE = 'AH001';
        END IF;
        bounds := regexp_match(bound, '^FOR VALUES FROM \(''([^'']+)''\) TO \(''([^'']+)''\)$');
        IF bounds IS NULL THEN
            RAISE EXCEPTION 'audit history drop refused: partition % has no timestamp range bounds', part_name
                USING ERRCODE = 'AH001';
        END IF;
        upper_bound := bounds[2]::timestamp with time zone;
        IF upper_bound > cutoff THEN
            RAISE EXCEPTION 'audit history drop refused: partition % ends at %, after the cutoff %', part_name, upper_bound, cutoff
                USING ERRCODE = 'AH001';
        END IF;

        EXECUTE format('ALTER TABLE public.%I NO FORCE ROW LEVEL SECURITY', part_name);
        EXECUTE format('SELECT count(*) FROM public.%I', part_name) INTO actual;
        IF actual <> wanted THEN
            RAISE EXCEPTION 'audit history drop refused: partition % holds % rows, % were verified', part_name, actual, wanted
                USING ERRCODE = 'AH001';
        END IF;
    END LOOP;

    FOR part_name IN SELECT n FROM unnest(names) AS n ORDER BY n LOOP
        EXECUTE format('DROP TABLE public.%I', part_name);
        dropped := dropped || part_name;
    END LOOP;
    RETURN dropped;
END;
$$;

REVOKE ALL ON FUNCTION public.audit_events_drop_verified_partitions(timestamp with time zone, text[], bigint[]) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.audit_events_drop_verified_partitions(timestamp with time zone, text[], bigint[]) TO app_control_plane;
