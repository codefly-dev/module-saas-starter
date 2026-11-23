-- Returns public.record_delivery_event to the role applying the ledger, under
-- which it advances an invitation's delivery status only where that role is a
-- superuser. That role may not take a function from app_control_plane, which
-- it is not a member of with inheritance, so the function is dropped (the
-- database owner may drop anything in public) and created again as it stood.
SET LOCAL lock_timeout = '15s';
DROP FUNCTION public.record_delivery_event(text,text,text,text,text,timestamp with time zone,uuid);
CREATE FUNCTION public.record_delivery_event(p_provider text, p_event_id text, p_provider_email_id text, p_event_type text, p_delivery_status text, p_event_created_at timestamp with time zone, p_invitation_id uuid)
 RETURNS boolean
 LANGUAGE plpgsql
 SECURITY DEFINER
 SET search_path TO 'pg_catalog', 'public', 'pg_temp'
AS $function$
DECLARE
    inserted_count BIGINT := 0;
BEGIN
    INSERT INTO public.email_delivery_events (
        provider,
        event_id,
        provider_email_id,
        event_type,
        delivery_status,
        invitation_id,
        event_created_at
    )
    VALUES (
        p_provider,
        p_event_id,
        p_provider_email_id,
        p_event_type,
        p_delivery_status,
        p_invitation_id,
        p_event_created_at
    )
    ON CONFLICT (provider, event_id) DO NOTHING;

    GET DIAGNOSTICS inserted_count = ROW_COUNT;
    IF inserted_count = 0 OR p_invitation_id IS NULL OR p_delivery_status IS NULL THEN
        RETURN inserted_count = 1;
    END IF;

    UPDATE public.invitations
    SET delivery_status = p_delivery_status
    WHERE id = p_invitation_id
      AND CASE delivery_status
            WHEN 'disabled' THEN 0
            WHEN 'queued' THEN 1
            WHEN 'sent' THEN 2
            WHEN 'delivered' THEN 3
            WHEN 'bounced' THEN 4
            WHEN 'complained' THEN 5
            ELSE -1
          END
          <=
          CASE p_delivery_status
            WHEN 'sent' THEN 2
            WHEN 'delivered' THEN 3
            WHEN 'bounced' THEN 4
            WHEN 'complained' THEN 5
            ELSE -1
          END;

    RETURN inserted_count = 1;
END;
$function$;

REVOKE ALL ON FUNCTION public.record_delivery_event(text,text,text,text,text,timestamp with time zone,uuid) FROM PUBLIC;
GRANT ALL ON FUNCTION public.record_delivery_event(text,text,text,text,text,timestamp with time zone,uuid) TO app_job_worker;
REVOKE INSERT, SELECT (provider, event_id) ON TABLE public.email_delivery_events FROM app_control_plane;
