REVOKE EXECUTE ON FUNCTION public.record_resend_delivery_event(
    TEXT, TEXT, TEXT, TIMESTAMPTZ, UUID
) FROM app_job_worker;
DROP FUNCTION IF EXISTS public.record_resend_delivery_event(
    TEXT, TEXT, TEXT, TIMESTAMPTZ, UUID
);
DROP TABLE IF EXISTS public.email_delivery_events;
