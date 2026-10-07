-- The queue holds the only copy of an event the relay has not yet delivered, so
-- removing it removes those events. Under the default (postgres) and the tee
-- (both) nothing writes it, and rolling back an empty queue is safe; with rows
-- in it the rollback refuses. Restore a warehouse sink and let the relay drain
-- the queue to depth 0 (MEASUREMENT_RUNBOOKS.md, audit-relay), then roll back.
--
-- The check reads as the relay's role: the queue forces row level security, and
-- that role's policy admits every row, so the count is the real one rather than
-- whatever the migrating session's own policies happen to show it.
--
-- The table is locked first, before anything is counted. A writer that has
-- inserted but not committed is invisible to the count, and DROP TABLE waits for
-- it and then drops the table beneath the row it committed. Taking the lock
-- first makes this wait for that writer instead, and the count that follows sees
-- what it committed; nothing can insert between the count and the drop, because
-- the lock is held until this transaction ends. The lock is taken as the
-- migrating role, which owns the table, before the role switch below.
LOCK TABLE public.audit_event_queue IN ACCESS EXCLUSIVE MODE;
SET LOCAL ROLE app_job_worker;
DO $$
DECLARE
    queued bigint;
BEGIN
    SELECT count(*) INTO queued FROM public.audit_event_queue;
    IF queued > 0 THEN
        RAISE EXCEPTION 'audit_event_queue still holds % queued audit events; refusing to drop the only copy of them. Restore a warehouse AUDIT_SINK and let the audit relay drain the queue to zero first.', queued;
    END IF;
END
$$;
RESET ROLE;
DROP TABLE IF EXISTS public.audit_event_queue;
