-- Domain-event ordering, dead-lettering, and relay fairness (review of #488).
--
-- Three defects of the 119 outbox are fixed together because all three live on
-- the relay's read path and one index serves all of them.
--
--   * ORDERING. domain_events.seq is BIGINT GENERATED ALWAYS AS IDENTITY, so it
--     is assigned at INSERT, not at COMMIT. Two concurrent producers writing one
--     partition can therefore commit out of seq order: tx A takes seq=10 and
--     commits second, tx B takes seq=11 and commits first, and a relay tick
--     landing between the two commits fans 11 out while 10 is still invisible.
--     The ordered subscriber then sees one tenant's lifecycle reordered — which
--     is precisely the guarantee business/events_producer.go advertises. This
--     migration makes publish_domain_event take a transaction-scoped advisory
--     lock on the partition before inserting (the pg_advisory_xact_lock pattern
--     billing/pg/store.go already uses for per-key serialization), so concurrent
--     same-partition publishes serialize and seq order equals commit order.
--     Distinct partitions hash to distinct keys and never contend, and a
--     platform event with no partition key takes no lock at all.
--
--   * DEAD-LETTERING. A fan-out that fails deterministically (a poison event) is
--     rolled back to its savepoint and left unpublished, so the next relay tick
--     selects it first and fails again — forever. Because per-partition ordering
--     holds every later event of that partition behind it, one bad row stalls a
--     tenant's whole event stream silently and permanently. There was no attempt
--     counter and no terminal state to escape to. relay_attempts,
--     last_relay_error, and dead_lettered_at give the relay a bounded retry
--     budget and a parked state: after the cap the event is set aside, the
--     partition unblocks, and the row survives for an operator to inspect and
--     replay.
--
--   * FAIRNESS. The relay read was ORDER BY (partition_key, seq) — alphabetical
--     by partition, not FIFO. A partition sorting early with a large backlog is
--     drained ahead of every other partition on every tick, so one busy tenant
--     starves the rest indefinitely. The relay now reads ORDER BY seq, which is
--     global publish order and still yields each individual partition's events
--     in their own seq order, so per-partition ordering is unaffected. The
--     unpublished index is recreated to serve that scan and to drop parked rows
--     out of it, keeping it small.

ALTER TABLE public.domain_events
    -- How many times the relay has tried and failed to fan this event out. Only
    -- a failed attempt increments it; a success sets published_at and the row
    -- leaves the relay's scan for good.
    ADD COLUMN relay_attempts   INTEGER NOT NULL DEFAULT 0,
    -- The most recent fan-out failure, kept for the operator who has to decide
    -- whether a parked event is replayable.
    ADD COLUMN last_relay_error TEXT NOT NULL DEFAULT '',
    -- Set when relay_attempts reached the cap: the event is parked, no longer
    -- selected by the relay, and no longer blocking its partition.
    ADD COLUMN dead_lettered_at TIMESTAMPTZ,
    ADD CONSTRAINT domain_events_relay_attempts_check
        CHECK (relay_attempts >= 0);

-- The relay drains unpublished, not-yet-parked events in global seq order; this
-- index serves exactly that scan and stays small because both relayed and parked
-- rows drop out of it.
DROP INDEX public.idx_domain_events_unpublished;
CREATE INDEX idx_domain_events_unpublished
    ON public.domain_events (seq)
    WHERE published_at IS NULL AND dead_lettered_at IS NULL;

-- Parked events are the operator's queue: small, and read by exactly this
-- predicate.
CREATE INDEX idx_domain_events_dead_lettered
    ON public.domain_events (dead_lettered_at)
    WHERE dead_lettered_at IS NOT NULL;

-- Replaced only to add the partition advisory lock; the caller-role gate, the
-- ON CONFLICT(id) idempotency, and the returned shape are unchanged from 119.
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
