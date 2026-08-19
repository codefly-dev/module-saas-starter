-- Reverse migration 99: restore the Resend-specific delivery-events shape from
-- migration 84.
--
-- The target schema forbids any provider other than 'resend' (the restored
-- CHECK), so reverting is necessarily destructive to non-Resend delivery
-- history: those rows cannot exist under the Resend-only shape. Rather than
-- assume none were written (an assumption this very migration's forward
-- direction exists to falsify) and let the ADD CONSTRAINT fail on them, delete
-- them explicitly here. Deletion touches only the delivery-event ledger; the
-- invitation delivery_status it projected onto is left as-is.
REVOKE EXECUTE ON FUNCTION public.record_delivery_event(
    TEXT, TEXT, TEXT, TEXT, TEXT, TIMESTAMPTZ, UUID
) FROM app_job_worker;
DROP FUNCTION IF EXISTS public.record_delivery_event(
    TEXT, TEXT, TEXT, TEXT, TEXT, TIMESTAMPTZ, UUID
);

-- Discard non-Resend history so the restored Resend-only CHECKs validate. Rows
-- with provider = 'resend' always carry a Resend event_type (only the Resend
-- adapter writes them), so the restored event_type enum holds for the survivors.
DELETE FROM public.email_delivery_events
WHERE provider IS DISTINCT FROM 'resend';

ALTER TABLE public.email_delivery_events
    DROP COLUMN delivery_status;

ALTER TABLE public.email_delivery_events
    DROP CONSTRAINT email_delivery_events_event_type_check;
ALTER TABLE public.email_delivery_events
    ADD CONSTRAINT email_delivery_events_event_type_check
        CHECK (event_type IN (
            'email.sent',
            'email.delivered',
            'email.delivery_delayed',
            'email.failed',
            'email.bounced',
            'email.complained',
            'email.opened',
            'email.clicked',
            'email.suppressed'
        ));
ALTER TABLE public.email_delivery_events
    DROP CONSTRAINT email_delivery_events_provider_check;
ALTER TABLE public.email_delivery_events
    ADD CONSTRAINT email_delivery_events_provider_check
        CHECK (provider = 'resend');
ALTER TABLE public.email_delivery_events
    ALTER COLUMN provider SET DEFAULT 'resend';

ALTER TABLE public.email_delivery_events
    DROP CONSTRAINT email_delivery_events_pkey;
ALTER TABLE public.email_delivery_events
    DROP CONSTRAINT email_delivery_events_event_id_check;
ALTER TABLE public.email_delivery_events
    RENAME COLUMN event_id TO svix_id;
ALTER TABLE public.email_delivery_events
    ADD CONSTRAINT email_delivery_events_svix_id_check
        CHECK (length(svix_id) BETWEEN 1 AND 255);
ALTER TABLE public.email_delivery_events
    ADD CONSTRAINT email_delivery_events_pkey PRIMARY KEY (svix_id);

CREATE OR REPLACE FUNCTION public.record_resend_delivery_event(
    p_svix_id TEXT,
    p_event_type TEXT,
    p_provider_email_id TEXT,
    p_event_created_at TIMESTAMPTZ,
    p_invitation_id UUID
)
RETURNS BOOLEAN
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
DECLARE
    inserted_count BIGINT := 0;
    projected_status TEXT;
BEGIN
    INSERT INTO public.email_delivery_events (
        svix_id,
        provider_email_id,
        event_type,
        invitation_id,
        event_created_at
    )
    VALUES (
        p_svix_id,
        p_provider_email_id,
        p_event_type,
        p_invitation_id,
        p_event_created_at
    )
    ON CONFLICT (svix_id) DO NOTHING;

    GET DIAGNOSTICS inserted_count = ROW_COUNT;
    IF inserted_count = 0 OR p_invitation_id IS NULL THEN
        RETURN inserted_count = 1;
    END IF;

    projected_status := CASE p_event_type
        WHEN 'email.sent' THEN 'sent'
        WHEN 'email.delivered' THEN 'delivered'
        WHEN 'email.failed' THEN 'bounced'
        WHEN 'email.bounced' THEN 'bounced'
        WHEN 'email.suppressed' THEN 'bounced'
        WHEN 'email.complained' THEN 'complained'
        ELSE NULL
    END;

    IF projected_status IS NOT NULL THEN
        UPDATE public.invitations
        SET delivery_status = projected_status
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
              CASE projected_status
                WHEN 'sent' THEN 2
                WHEN 'delivered' THEN 3
                WHEN 'bounced' THEN 4
                WHEN 'complained' THEN 5
                ELSE -1
              END;
    END IF;

    RETURN inserted_count = 1;
END;
$$;

REVOKE ALL ON FUNCTION public.record_resend_delivery_event(
    TEXT, TEXT, TEXT, TIMESTAMPTZ, UUID
) FROM PUBLIC, app_tenant, app_control_plane, app_billing_worker,
       app_webhook_worker;
GRANT EXECUTE ON FUNCTION public.record_resend_delivery_event(
    TEXT, TEXT, TEXT, TIMESTAMPTZ, UUID
) TO app_job_worker;
