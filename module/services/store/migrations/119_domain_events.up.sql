-- Domain events pub/sub (issue #493, EVENTS.md Phasing P2).
--
-- Two relations plus one security-definer publish function turn the P1 envelope
-- contract into a working transactional outbox with fan-out:
--
--   * domain_events is the durable event-of-record. Publish inserts exactly one
--     row IN the producer's transaction (the same rule as today's outbox). It is
--     a TENANT relation: forced RLS on app.current_org_id, so a tenant never sees
--     another tenant's events. A monotonic seq drives the relay's read order and
--     published_at is NULL until the relay has fanned the event out.
--   * event_subscriptions is a control-plane-owned platform relation: who
--     receives which type on which queue. It is materialized from each catalog
--     `consumes` entry at install and edited at runtime through
--     ModuleCapabilitiesService.Subscribe / Unsubscribe. The relay (app_job_worker,
--     BYPASSRLS) resolves matching non-revoked rows for every event.
--   * publish_domain_event mirrors enqueue_job_message: SECURITY DEFINER, an
--     explicit caller-role gate (request traffic may publish only its own bound
--     tenant; the control plane only platform scope; the job worker anything, for
--     relay-time replay), and ON CONFLICT(id) idempotency keyed on the envelope id
--     with a fingerprint so a reused id carrying a different fact is rejected.
--
-- No schema change to job_messages: the relay enqueues one ordinary inbox job per
-- subscription, so the consume path (ClaimJobs / Ack / Nack) is unchanged.

CREATE TABLE public.domain_events (
    id                  UUID PRIMARY KEY,
    seq                 BIGINT GENERATED ALWAYS AS IDENTITY,
    type                TEXT NOT NULL,
    source              TEXT NOT NULL,
    subject             TEXT NOT NULL DEFAULT '',
    event_time          TIMESTAMPTZ,
    specversion         TEXT NOT NULL DEFAULT '',
    datacontenttype     TEXT NOT NULL DEFAULT '',
    dataschema          TEXT NOT NULL DEFAULT '',
    data                BYTEA NOT NULL DEFAULT ''::bytea,
    -- tenant_id is NULL for platform-scope events published by a platform
    -- principal; every tenant event carries its owning organization.
    tenant_id           UUID,
    boundary_id         TEXT NOT NULL DEFAULT '',
    partition_key       TEXT NOT NULL DEFAULT '',
    correlation_id      TEXT NOT NULL DEFAULT '',
    causation_id        TEXT NOT NULL DEFAULT '',
    actor_principal_id  TEXT NOT NULL DEFAULT '',
    owner_principal_id  TEXT NOT NULL DEFAULT '',
    traceparent         TEXT NOT NULL DEFAULT '',
    schema_version      INTEGER NOT NULL DEFAULT 1,
    request_fingerprint BYTEA NOT NULL,
    -- NULL until the relay has enqueued a delivery for every matching
    -- subscription; the relay reads WHERE published_at IS NULL.
    published_at        TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT domain_events_type_check
        CHECK (type ~ '^[a-z][a-z0-9]*(?:\.[a-z0-9]+)+$'),
    CONSTRAINT domain_events_source_check
        CHECK (source ~ '^[a-z][a-z0-9_.:/-]{0,254}$'),
    CONSTRAINT domain_events_fingerprint_check
        CHECK (length(request_fingerprint) = 32)
);

-- The relay drains unpublished events in (partition_key, seq) order; this index
-- serves exactly that scan and stays small because relayed rows drop out of it.
CREATE INDEX idx_domain_events_unpublished
    ON public.domain_events (partition_key, seq)
    WHERE published_at IS NULL;
-- Replay reads by (type, tenant, time); tenant events only.
CREATE INDEX idx_domain_events_replay
    ON public.domain_events (type, tenant_id, created_at)
    WHERE tenant_id IS NOT NULL;

ALTER TABLE public.domain_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.domain_events FORCE  ROW LEVEL SECURITY;

-- Tenant boundary: request traffic reads only its own organization's events.
-- Platform-scope rows (tenant_id IS NULL) are invisible to app_tenant and are
-- handled by the relay/control plane, which run BYPASSRLS. Publishing is
-- function-only (below), so there is no INSERT policy for app_tenant.
CREATE POLICY domain_events_tenant ON public.domain_events
    FOR SELECT
    USING (tenant_id::text = current_setting('app.current_org_id', true));

-- Request traffic never writes domain_events directly; it publishes through the
-- SECURITY DEFINER function and reads its own tenant's rows under RLS. The relay
-- worker (BYPASSRLS) marks rows published; the control plane maintains them.
REVOKE ALL PRIVILEGES ON public.domain_events FROM app_tenant;
GRANT SELECT ON public.domain_events TO app_tenant;
GRANT SELECT, UPDATE ON public.domain_events TO app_job_worker;
GRANT SELECT, INSERT, UPDATE ON public.domain_events TO app_control_plane;

-- event_subscriptions is a platform relation (not tenant-scoped): a subscription
-- belongs to a subscriber principal, and the relay must resolve subscriptions
-- across every tenant's events. No RLS; access is by role grant only.
CREATE TABLE public.event_subscriptions (
    id                      UUID DEFAULT gen_random_uuid() PRIMARY KEY,
    subscriber_principal_id UUID NOT NULL,
    type_pattern            TEXT NOT NULL,
    queue                   TEXT NOT NULL,
    delivery                TEXT NOT NULL DEFAULT 'unordered',
    filter                  JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_by              UUID,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at              TIMESTAMPTZ,
    CONSTRAINT event_subscriptions_delivery_check
        CHECK (delivery IN ('ordered', 'unordered')),
    -- An exact type, or a single trailing ".*" prefix; "*" mid-string is invalid.
    CONSTRAINT event_subscriptions_type_pattern_check
        CHECK (type_pattern ~ '^[a-z][a-z0-9]*(?:\.[a-z0-9]+)*(?:\.\*)?$'),
    -- Same queue grammar as job_messages, and events.relay is reserved for the
    -- relay itself: no subscription may claim it.
    CONSTRAINT event_subscriptions_queue_check
        CHECK (queue ~ '^[a-z][a-z0-9_.-]{0,127}$' AND queue <> 'events.relay')
);

-- At most one active subscription per (subscriber, pattern, queue): re-subscribing
-- the same shape is idempotent, and a revoked row leaves the way clear to
-- re-subscribe later.
CREATE UNIQUE INDEX idx_event_subscriptions_active
    ON public.event_subscriptions (subscriber_principal_id, type_pattern, queue)
    WHERE revoked_at IS NULL;
-- The relay resolves live subscriptions on every drain.
CREATE INDEX idx_event_subscriptions_live
    ON public.event_subscriptions (type_pattern)
    WHERE revoked_at IS NULL;

REVOKE ALL PRIVILEGES ON public.event_subscriptions FROM PUBLIC, app_tenant;
GRANT SELECT ON public.event_subscriptions TO app_job_worker;
GRANT SELECT, INSERT, UPDATE ON public.event_subscriptions TO app_control_plane;

-- Publish is the only write path to domain_events for request traffic. Like
-- enqueue_job_message it is SECURITY DEFINER and checks the caller role and the
-- signed org scope explicitly, because DEFINER bypasses the table RLS above.
CREATE FUNCTION public.publish_domain_event(
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

REVOKE ALL ON FUNCTION public.publish_domain_event(
    UUID, TEXT, TEXT, TEXT, TIMESTAMPTZ, TEXT, TEXT, TEXT, BYTEA, UUID, TEXT,
    TEXT, TEXT, TEXT, TEXT, TEXT, TEXT, INTEGER, BYTEA
) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.publish_domain_event(
    UUID, TEXT, TEXT, TEXT, TIMESTAMPTZ, TEXT, TEXT, TEXT, BYTEA, UUID, TEXT,
    TEXT, TEXT, TEXT, TEXT, TEXT, TEXT, INTEGER, BYTEA
) TO app_tenant, app_control_plane, app_job_worker;
