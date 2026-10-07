-- Nothing deletes a quarantined row, and rolling this back would. With rows in
-- the quarantine the rollback refuses: replay or resolve them first.
--
-- The check reads as the relay's role, the only one the quarantine's row level
-- security admits.
--
-- The table is locked first, before anything is counted. A relay that has moved
-- a row in but not committed is invisible to the count, and DROP TABLE waits for
-- it and then drops the table beneath the row it committed. Taking the lock
-- first makes this wait for that transaction instead, and the count that follows
-- sees what it committed; nothing can insert between the count and the drop,
-- because the lock is held until this transaction ends. The lock is taken as the
-- migrating role, which owns the table, before the role switch below.
LOCK TABLE public.audit_event_quarantine IN ACCESS EXCLUSIVE MODE;
SET LOCAL ROLE app_job_worker;
DO $$
DECLARE
    quarantined bigint;
BEGIN
    SELECT count(*) INTO quarantined FROM public.audit_event_quarantine;
    IF quarantined > 0 THEN
        RAISE EXCEPTION 'audit_event_quarantine still holds % audit events the relay set aside; refusing to drop the only copy of them. Replay or resolve them first.', quarantined;
    END IF;
END
$$;
RESET ROLE;
DROP TABLE IF EXISTS public.audit_event_quarantine;
