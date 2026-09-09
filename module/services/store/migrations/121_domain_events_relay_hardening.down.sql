-- Reverse 121: restore the 119 publish function (no partition advisory lock),
-- the 119 unpublished index, and drop the relay dead-letter columns. The parked
-- index and the columns go first because the unpublished index predicate below
-- must no longer reference dead_lettered_at.

DROP INDEX public.idx_domain_events_dead_lettered;
DROP INDEX public.idx_domain_events_unpublished;

ALTER TABLE public.domain_events
    DROP CONSTRAINT domain_events_relay_attempts_check,
    DROP COLUMN relay_attempts,
    DROP COLUMN last_relay_error,
    DROP COLUMN dead_lettered_at;

CREATE INDEX idx_domain_events_unpublished
    ON public.domain_events (partition_key, seq)
    WHERE published_at IS NULL;

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
