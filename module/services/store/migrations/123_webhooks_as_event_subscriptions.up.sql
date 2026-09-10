-- Outbound webhooks become domain-event subscriptions (issue #488, EVENTS.md P3).
--
-- Before this migration there were two fan-out mechanisms. The audit emitter
-- matched webhook_subscriptions.events inline and wrote the outbox itself, while
-- domain events fanned out through the relay over event_subscriptions. An
-- endpoint was therefore invisible to everything the events contract provides:
-- the admin Events page, ListEventSubscriptions, replay, and the per-subscription
-- lag view all saw module subscribers only.
--
-- After it there is one registry. A webhook endpoint subscribed to N event names
-- is N rows in event_subscriptions with delivery = 'webhook', and the relay is
-- the only code that fans an event out. The dispatcher below it is untouched:
-- the relay creates the same webhook_deliveries row and enqueues the same
-- saas.webhooks.v1.OutboundWebhookJob the audit emitter used to, so exact-byte
-- signing, key rotation, the attempt schedule, and delivery history all keep
-- working on rows that predate the cutover.
--
-- Three schema changes make that possible.
--
--   * GRAMMAR. domain_events.type and event_subscriptions.type_pattern required
--     dotted segments of [a-z0-9] only, but 56 of the 127 registered audit event
--     types carry an underscore (saas.api_key.created,
--     saas.auth.mfa_challenge_completed, ...). Publishing those as the external
--     domain events a webhook subscribes to means the grammar has to admit the
--     names the audit registry already mints. Underscore is added inside a
--     segment; the segment structure is unchanged, so no existing value stops
--     validating.
--
--   * SUBSCRIBER KIND. event_subscriptions described exactly one kind of
--     subscriber: a module principal, cross-tenant, delivered on its own queue.
--     A webhook subscriber is neither — it belongs to one organization and its
--     consumer is the shared dispatcher — so subscriber_principal_id becomes
--     nullable and org_id / webhook_subscription_id arrive beside it, each
--     constrained to exactly the kind that owns it. org_id is what stops a
--     tenant's event reaching another tenant's endpoint; the relay filters on it
--     rather than trusting the pattern match alone.
--
--   * IDEMPOTENCY. The existing partial unique index keys on
--     subscriber_principal_id, which is NULL for every webhook row, and NULLs do
--     not collide in a unique index. A second partial index gives webhook rows
--     the same "re-subscribing the same shape is a no-op" property the module
--     rows already have.
--
-- The backfill then makes every existing registration a subscription. A stored
-- event name that cannot be a domain event type is skipped rather than rejected:
-- webhook names permit hyphens and are accepted even when they match no
-- registered event, so such a name has never fired and cannot start now. Skipping
-- it keeps the migration total over whatever is in the table.

ALTER TABLE public.domain_events
    DROP CONSTRAINT domain_events_type_check,
    ADD  CONSTRAINT domain_events_type_check
        CHECK (type ~ '^[a-z][a-z0-9_]*(?:\.[a-z0-9_]+)+$');

ALTER TABLE public.event_subscriptions
    DROP CONSTRAINT event_subscriptions_type_pattern_check,
    ADD  CONSTRAINT event_subscriptions_type_pattern_check
        CHECK (type_pattern ~ '^[a-z][a-z0-9_]*(?:\.[a-z0-9_]+)*(?:\.\*)?$');

ALTER TABLE public.event_subscriptions
    ALTER COLUMN subscriber_principal_id DROP NOT NULL,
    -- The organization whose events this subscription may receive. NULL is a
    -- module subscription, which is cross-tenant by construction; a webhook
    -- subscription is bound to the tenant that registered the endpoint.
    ADD COLUMN org_id UUID REFERENCES public.organizations(id) ON DELETE CASCADE,
    -- The endpoint registration that owns this subscription. Deleting the
    -- registration removes its subscriptions, which is what makes DeleteSubscription
    -- stop delivery without a second write.
    ADD COLUMN webhook_subscription_id UUID
        REFERENCES public.webhook_subscriptions(id) ON DELETE CASCADE;

ALTER TABLE public.event_subscriptions
    DROP CONSTRAINT event_subscriptions_delivery_check,
    ADD  CONSTRAINT event_subscriptions_delivery_check
        CHECK (delivery IN ('ordered', 'unordered', 'webhook')),
    -- Each subscriber kind carries exactly its own columns, so a row can never
    -- be half a module subscription and half a webhook one.
    ADD  CONSTRAINT event_subscriptions_subscriber_kind_check
        CHECK (
            (delivery = 'webhook'
                AND webhook_subscription_id IS NOT NULL
                AND org_id IS NOT NULL
                AND subscriber_principal_id IS NULL)
            OR
            (delivery <> 'webhook'
                AND webhook_subscription_id IS NULL
                AND org_id IS NULL
                AND subscriber_principal_id IS NOT NULL)
        );

CREATE UNIQUE INDEX idx_event_subscriptions_webhook_active
    ON public.event_subscriptions (webhook_subscription_id, type_pattern)
    WHERE revoked_at IS NULL AND webhook_subscription_id IS NOT NULL;

-- The relay resolves the endpoint and writes delivery history inside its own
-- fan-out transaction, so it needs the same reads the dispatcher has plus the
-- insert the audit emitter used to perform. Attempt outcomes stay with
-- app_webhook_worker: the relay creates a delivery, it never reports on one.
GRANT SELECT ON public.webhook_subscriptions TO app_job_worker;
GRANT SELECT, INSERT ON public.webhook_deliveries TO app_job_worker;

INSERT INTO public.event_subscriptions (
    subscriber_principal_id, type_pattern, queue, delivery, org_id, webhook_subscription_id
)
SELECT NULL::uuid, event_name, 'webhooks', 'webhook', subscription.org_id, subscription.id
FROM public.webhook_subscriptions AS subscription
CROSS JOIN LATERAL unnest(subscription.events) AS event_name
WHERE event_name ~ '^[a-z][a-z0-9_]*(?:\.[a-z0-9_]+)+$'
ON CONFLICT DO NOTHING;

-- Let the control plane publish a tenant's event; see the branch comment below.
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
        -- IS DISTINCT FROM, not <>: a transaction that never set app.current_org_id
        -- (a user-scoped one, which runs as app_tenant and sets only
        -- app.current_user_id) leaves the setting NULL, and `p_tenant_id <> NULL`
        -- is NULL, not TRUE — so a plain comparison lets that transaction publish
        -- for any tenant it names. The guard has to fail closed on an absent scope.
        IF p_tenant_id IS NULL
           OR p_tenant_id IS DISTINCT FROM NULLIF(current_setting('app.current_org_id', true), '')::uuid THEN
            RAISE EXCEPTION 'event tenant does not match the signed request scope'
                USING ERRCODE = 'insufficient_privilege';
        END IF;
    ELSIF caller_role = 'app_control_plane' THEN
        -- The control plane publishes on behalf of a tenant. A platform-admin
        -- mutation writes its audit record with RLS bypassed, and that record's
        -- external domain event is what an outbound webhook subscription is
        -- fanned out from, so refusing a tenant here would silently stop
        -- delivering every privileged action to the endpoints subscribed to it.
        -- The role already writes any tenant's audit rows; publishing the
        -- matching event is the same authority, and the relay applies the tenant
        -- gate again when it resolves subscriptions.
        NULL;
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

-- event_subscriptions is control-plane owned and has no RLS, so request traffic
-- cannot write it directly — but an endpoint's subscriptions have to be derived
-- inside the registration's own transaction, or a crash between the two leaves an
-- endpoint that is registered and never delivered to. This is the same shape
-- publish_domain_event uses for the same reason: SECURITY DEFINER, with the
-- caller's role and signed org scope checked explicitly because DEFINER bypasses
-- the grants above.
CREATE FUNCTION public.sync_webhook_event_subscriptions(
    p_org_id                  UUID,
    p_webhook_subscription_id UUID,
    p_type_patterns           TEXT[]
)
RETURNS VOID
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $function$
DECLARE
    caller_role TEXT;
    owner_org   UUID;
BEGIN
    caller_role := COALESCE(
        NULLIF(current_setting('role', true), 'none'),
        session_user::text
    );

    IF caller_role = 'app_tenant' THEN
        IF p_org_id IS NULL
           OR p_org_id <> NULLIF(current_setting('app.current_org_id', true), '')::uuid THEN
            RAISE EXCEPTION 'webhook subscription org does not match the signed request scope'
                USING ERRCODE = 'insufficient_privilege';
        END IF;
    ELSIF caller_role NOT IN ('app_control_plane', 'app_job_worker') THEN
        RAISE EXCEPTION 'role % cannot manage webhook event subscriptions', caller_role
            USING ERRCODE = 'insufficient_privilege';
    END IF;

    -- The endpoint must belong to the org the subscriptions are being bound to,
    -- so a caller holding one org's scope cannot attach a subscription to another
    -- org's endpoint by naming its id.
    SELECT org_id INTO owner_org
    FROM public.webhook_subscriptions
    WHERE id = p_webhook_subscription_id;
    IF owner_org IS NULL OR owner_org <> p_org_id THEN
        RAISE EXCEPTION 'webhook subscription does not belong to this organization'
            USING ERRCODE = 'insufficient_privilege';
    END IF;

    DELETE FROM public.event_subscriptions
    WHERE webhook_subscription_id = p_webhook_subscription_id
      AND type_pattern <> ALL(COALESCE(p_type_patterns, ARRAY[]::text[]));

    INSERT INTO public.event_subscriptions
        (subscriber_principal_id, type_pattern, queue, delivery, org_id, webhook_subscription_id)
    SELECT NULL::uuid, pattern, 'webhooks', 'webhook', p_org_id, p_webhook_subscription_id
    FROM unnest(COALESCE(p_type_patterns, ARRAY[]::text[])) AS pattern
    ON CONFLICT DO NOTHING;
END
$function$;

REVOKE ALL ON FUNCTION public.sync_webhook_event_subscriptions(UUID, UUID, TEXT[]) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.sync_webhook_event_subscriptions(UUID, UUID, TEXT[])
    TO app_tenant, app_control_plane, app_job_worker;
