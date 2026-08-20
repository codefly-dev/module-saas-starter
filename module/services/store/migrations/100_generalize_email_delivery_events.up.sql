-- Generalize email delivery events beyond Resend so a second webhook-capable
-- provider can persist and project delivery state without a schema change.
--
-- Migration 84 pinned this table to Resend three ways: CHECK (provider =
-- 'resend'), a Resend-namespaced event_type enum, and record_resend_delivery_
-- event, which decoded Resend's event vocabulary into an invitation status.
-- Adapters now translate their native events into a canonical delivery_status
-- before persistence, so the projection here is provider-agnostic and the
-- dedup key is provider-scoped rather than Svix-specific.

-- 1. Provider-scope the dedup key. Svix ids are Resend-specific; a second
--    provider carries its own event id. Rename svix_id -> event_id and make the
--    primary key (provider, event_id) so ids from different providers cannot
--    collide.
ALTER TABLE public.email_delivery_events
    DROP CONSTRAINT email_delivery_events_pkey;
ALTER TABLE public.email_delivery_events
    RENAME COLUMN svix_id TO event_id;
ALTER TABLE public.email_delivery_events
    DROP CONSTRAINT email_delivery_events_svix_id_check;
ALTER TABLE public.email_delivery_events
    ADD CONSTRAINT email_delivery_events_event_id_check
        CHECK (length(event_id) BETWEEN 1 AND 255);
ALTER TABLE public.email_delivery_events
    ADD CONSTRAINT email_delivery_events_pkey PRIMARY KEY (provider, event_id);

-- 2. Drop the Resend-only value constraints. The provider name and the native
--    event vocabulary are provider-specific; keep length bounds only.
ALTER TABLE public.email_delivery_events
    ALTER COLUMN provider DROP DEFAULT;
ALTER TABLE public.email_delivery_events
    DROP CONSTRAINT email_delivery_events_provider_check;
ALTER TABLE public.email_delivery_events
    ADD CONSTRAINT email_delivery_events_provider_check
        CHECK (length(provider) BETWEEN 1 AND 64);
ALTER TABLE public.email_delivery_events
    DROP CONSTRAINT email_delivery_events_event_type_check;
ALTER TABLE public.email_delivery_events
    ADD CONSTRAINT email_delivery_events_event_type_check
        CHECK (length(event_type) BETWEEN 1 AND 64);

-- 3. Persist the canonical status the adapter computed. NULL means the native
--    event does not advance delivery state (e.g. opened/clicked/delayed): the
--    event is still recorded for audit, but no invitation projection runs.
ALTER TABLE public.email_delivery_events
    ADD COLUMN delivery_status TEXT
        CHECK (delivery_status IS NULL OR delivery_status IN
            ('sent', 'delivered', 'bounced', 'complained'));

-- 4. Replace the Resend-specific projection with a provider-agnostic one. The
--    adapter passes the already-canonical status; this function only dedups and
--    advances the invitation monotonically. The old function referenced the
--    renamed column and is dropped.
DROP FUNCTION IF EXISTS public.record_resend_delivery_event(
    TEXT, TEXT, TEXT, TIMESTAMPTZ, UUID
);

CREATE OR REPLACE FUNCTION public.record_delivery_event(
    p_provider TEXT,
    p_event_id TEXT,
    p_provider_email_id TEXT,
    p_event_type TEXT,
    p_delivery_status TEXT,
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
$$;

REVOKE ALL ON FUNCTION public.record_delivery_event(
    TEXT, TEXT, TEXT, TEXT, TEXT, TIMESTAMPTZ, UUID
) FROM PUBLIC, app_tenant, app_control_plane, app_billing_worker,
       app_webhook_worker;
GRANT EXECUTE ON FUNCTION public.record_delivery_event(
    TEXT, TEXT, TEXT, TEXT, TEXT, TIMESTAMPTZ, UUID
) TO app_job_worker;
