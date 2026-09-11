-- Reverse 123. The webhook subscription rows go first: they are the only rows
-- that violate the constraints being restored, and the audit emitter's inline
-- fan-out is what delivers webhooks again once this migration is off.

DROP FUNCTION IF EXISTS public.sync_webhook_event_subscriptions(UUID, UUID, TEXT[]);

DELETE FROM public.event_subscriptions WHERE delivery = 'webhook';

REVOKE SELECT, INSERT ON public.webhook_deliveries FROM app_job_worker;
REVOKE SELECT ON public.webhook_subscriptions FROM app_job_worker;

DROP INDEX IF EXISTS public.idx_event_subscriptions_wildcard;
DROP INDEX IF EXISTS public.idx_event_subscriptions_webhook_active;

ALTER TABLE public.event_subscriptions
    DROP CONSTRAINT event_subscriptions_subscriber_kind_check,
    DROP CONSTRAINT event_subscriptions_delivery_check,
    ADD  CONSTRAINT event_subscriptions_delivery_check
        CHECK (delivery IN ('ordered', 'unordered'));

ALTER TABLE public.event_subscriptions
    DROP COLUMN webhook_subscription_id,
    DROP COLUMN org_id,
    ALTER COLUMN subscriber_principal_id SET NOT NULL;

-- A type carrying an underscore cannot exist under the restored grammar, so the
-- external events published for audit types are dropped with it. They are a
-- 30-day replay window, not a system of record: the audit_events row each one
-- was published beside is untouched.
DELETE FROM public.domain_events WHERE type !~ '^[a-z][a-z0-9]*(?:\.[a-z0-9]+)+$';
DELETE FROM public.event_subscriptions WHERE type_pattern !~ '^[a-z][a-z0-9]*(?:\.[a-z0-9]+)*(?:\.\*)?$';

ALTER TABLE public.event_subscriptions
    DROP CONSTRAINT event_subscriptions_type_pattern_check,
    ADD  CONSTRAINT event_subscriptions_type_pattern_check
        CHECK (type_pattern ~ '^[a-z][a-z0-9]*(?:\.[a-z0-9]+)*(?:\.\*)?$');

ALTER TABLE public.domain_events
    DROP CONSTRAINT domain_events_type_check,
    ADD  CONSTRAINT domain_events_type_check
        CHECK (type ~ '^[a-z][a-z0-9]*(?:\.[a-z0-9]+)+$');

-- Restore the platform-scope-only rule for control-plane publishes.
CREATE OR REPLACE FUNCTION public.publish_domain_event(
    p_id                 UUID,
    p_type               TEXT,
    p_source             TEXT,
    p_subject            TEXT,
    p_event_time         TIMESTAMPTZ,
    p_specversion        TEXT,
    p_datacontenttype    TEXT,
    p_dataschema         TEXT,
    p_data               BYTEA,
    p_tenant_id          UUID,
    p_boundary_id        TEXT,
    p_partition_key      TEXT,
    p_correlation_id     TEXT,
    p_causation_id       TEXT,
    p_actor_principal_id TEXT,
    p_owner_principal_id TEXT,
    p_traceparent        TEXT,
    p_schema_version     INTEGER,
    p_request_fingerprint BYTEA
)
RETURNS TABLE(event_id UUID, stored_fingerprint BYTEA, inserted BOOLEAN)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $function$
DECLARE
    caller_role TEXT;
    inserted_id UUID;
BEGIN
    caller_role := COALESCE(
        NULLIF(current_setting('role', true), 'none'),
        session_user::text
    );

    IF caller_role = 'app_tenant' THEN
        IF p_tenant_id IS NULL
           OR p_tenant_id <> NULLIF(current_setting('app.current_org_id', true), '')::uuid THEN
            RAISE EXCEPTION 'event tenant does not match the signed request scope'
                USING ERRCODE = 'insufficient_privilege';
        END IF;
    ELSIF caller_role = 'app_control_plane' THEN
        IF p_tenant_id IS NOT NULL THEN
            RAISE EXCEPTION 'control-plane traffic may publish platform-scope events only'
                USING ERRCODE = 'insufficient_privilege';
        END IF;
    ELSIF caller_role <> 'app_job_worker' THEN
        RAISE EXCEPTION 'role % cannot publish domain events', caller_role
            USING ERRCODE = 'insufficient_privilege';
    END IF;

    -- Serialize concurrent publishes within one partition so that the identity
    -- seq assigned below is handed out in commit order, not merely in insert
    -- order. The lock is transaction-scoped: it is held until the producer's
    -- transaction commits or rolls back, which is exactly the window in which a
    -- competing producer could otherwise take a higher seq and commit first.
    -- Ordering is a per-partition guarantee, so the lock is per-partition; a
    -- platform event with no partition key is unordered by construction
    -- (eventOrdering returns no ordering key for it) and takes no lock, so it
    -- never serializes against anything.
    IF COALESCE(p_partition_key, '') <> '' THEN
        PERFORM pg_advisory_xact_lock(hashtextextended('events:' || p_partition_key, 0));
    END IF;

    INSERT INTO public.domain_events (
        id, type, source, subject, event_time, specversion, datacontenttype,
        dataschema, data, tenant_id, boundary_id, partition_key, correlation_id,
        causation_id, actor_principal_id, owner_principal_id, traceparent,
        schema_version, request_fingerprint
    ) VALUES (
        p_id, p_type, p_source, COALESCE(p_subject, ''), p_event_time,
        COALESCE(p_specversion, ''), COALESCE(p_datacontenttype, ''),
        COALESCE(p_dataschema, ''), COALESCE(p_data, ''::bytea), p_tenant_id,
        COALESCE(p_boundary_id, ''), COALESCE(p_partition_key, ''),
        COALESCE(p_correlation_id, ''), COALESCE(p_causation_id, ''),
        COALESCE(p_actor_principal_id, ''), COALESCE(p_owner_principal_id, ''),
        COALESCE(p_traceparent, ''), COALESCE(p_schema_version, 1),
        p_request_fingerprint
    )
    ON CONFLICT (id) DO NOTHING
    RETURNING id INTO inserted_id;

    IF inserted_id IS NOT NULL THEN
        RETURN QUERY SELECT inserted_id, p_request_fingerprint, TRUE;
        RETURN;
    END IF;

    RETURN QUERY
    SELECT existing.id, existing.request_fingerprint, FALSE
    FROM public.domain_events AS existing
    WHERE existing.id = p_id;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'domain event idempotency conflict could not be resolved'
            USING ERRCODE = 'serialization_failure';
    END IF;
END
$function$;
