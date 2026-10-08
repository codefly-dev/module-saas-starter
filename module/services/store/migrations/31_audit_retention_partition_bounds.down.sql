-- Reverts migration 31: audit_events_drop_partitions_before returns to choosing
-- partitions by the month in their name, read in the calling session's time
-- zone, and to dropping them without a lock or a lock_timeout. That reopens both
-- defects migration 31 closed: a partition created in a zone behind UTC can be
-- dropped up to that many hours before its last event is older than the cutoff,
-- and a writer that does not finish hangs retention.
--
-- The function is restored as it stood after migration 12, search_path included,
-- which lists pg_temp last; CREATE OR REPLACE takes the SET lock_timeout with it.
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
