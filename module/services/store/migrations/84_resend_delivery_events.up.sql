-- Privacy-minimized, replay-safe Resend delivery history.
--
-- The public webhook verifies the exact raw body before invoking this
-- projection. We retain only operational identifiers and timestamps: no
-- recipient address, subject, body, click URL, bounce text, or raw payload.

CREATE TABLE public.email_delivery_events (
    svix_id TEXT PRIMARY KEY
        CHECK (length(svix_id) BETWEEN 1 AND 255),
    provider TEXT NOT NULL DEFAULT 'resend'
        CHECK (provider = 'resend'),
    provider_email_id TEXT NOT NULL
        CHECK (length(provider_email_id) BETWEEN 1 AND 255),
    event_type TEXT NOT NULL
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
        )),
    invitation_id UUID REFERENCES public.invitations(id) ON DELETE SET NULL,
    event_created_at TIMESTAMPTZ NOT NULL,
    received_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX email_delivery_events_provider_email
    ON public.email_delivery_events(provider_email_id, event_created_at DESC);
CREATE INDEX email_delivery_events_invitation
    ON public.email_delivery_events(invitation_id, event_created_at DESC)
    WHERE invitation_id IS NOT NULL;

REVOKE ALL PRIVILEGES ON public.email_delivery_events
FROM PUBLIC, app_tenant, app_control_plane, app_billing_worker,
     app_webhook_worker, app_job_worker;

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
