-- Reverts migration 32: audit_events_drop_partitions_before returns to choosing
-- partitions by the month in their name, read in the calling session's time
-- zone, and to dropping them without a lock or a lock_timeout. Retention then
-- holds none of migration 32's rules: what it drops depends on the zone of the
-- session that runs it and of the one that created each partition, it accepts a
-- cutoff that is not in the past, and a partition being detached, or detached
-- while it ran, is dropped like any other.
--
-- The function is restored as it stood after migration 12, search_path included,
-- which lists pg_temp last; CREATE OR REPLACE takes the SET lock_timeout,
-- DateStyle and TimeZone with it.
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
