-- Nothing deletes a quarantined row, and rolling this back would. With rows in
-- the quarantine the rollback refuses: replay or resolve them first.
--
-- The check reads as the relay's role, the only one the quarantine's row level
-- security admits.
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
