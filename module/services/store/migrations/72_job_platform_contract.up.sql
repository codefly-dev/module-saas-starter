-- Product-neutral durable inbox/outbox persistence contract.
--
-- This migration freezes the common envelope and finite state machines.
-- Generic execution operations live in Accounts' product-neutral job store.
-- Stripe webhook processing is the first workload on this contract; other
-- asynchronous workloads migrate in their explicit follow-up slices.

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'app_job_worker') THEN
        CREATE ROLE app_job_worker
            NOLOGIN NOINHERIT NOSUPERUSER BYPASSRLS
            NOCREATEDB NOCREATEROLE NOREPLICATION;
    ELSE
        ALTER ROLE app_job_worker
            NOLOGIN NOINHERIT NOSUPERUSER BYPASSRLS
            NOCREATEDB NOCREATEROLE NOREPLICATION;
    END IF;
END $$;

GRANT USAGE ON SCHEMA public TO app_job_worker;
REVOKE CREATE ON SCHEMA public FROM app_job_worker;

DO $$
BEGIN
    EXECUTE format(
        'REVOKE TEMPORARY, CONNECT ON DATABASE %I FROM app_job_worker',
        current_database()
    );
    EXECUTE format('GRANT app_job_worker TO %I', current_user);
END $$;

CREATE TABLE public.job_messages (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    direction TEXT NOT NULL,
    scope_kind TEXT NOT NULL,
    organization_id UUID,
    subject_id UUID,
    queue TEXT NOT NULL,
    topic TEXT NOT NULL,
    source TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    request_fingerprint BYTEA NOT NULL,
    ordering_key TEXT,
    schema_version INTEGER NOT NULL DEFAULT 1,
    payload BYTEA NOT NULL,
    content_type TEXT NOT NULL DEFAULT 'application/json',
    attributes JSONB NOT NULL DEFAULT '{}'::jsonb,
    state TEXT NOT NULL DEFAULT 'pending',
    priority SMALLINT NOT NULL DEFAULT 0,
    attempt_count INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 8,
    lease_owner TEXT,
    lease_token UUID,
    lease_expires_at TIMESTAMPTZ,
    heartbeat_at TIMESTAMPTZ,
    last_error_code TEXT,
    last_error_message TEXT,
    available_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_attempt_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    dead_lettered_at TIMESTAMPTZ,
    replay_of UUID REFERENCES public.job_messages(id) ON DELETE RESTRICT,
    state_version BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT job_messages_direction_check
        CHECK (direction IN ('inbox', 'outbox')),
    CONSTRAINT job_messages_scope_check CHECK (
        (scope_kind = 'tenant' AND organization_id IS NOT NULL AND subject_id IS NULL)
        OR (scope_kind = 'subject' AND organization_id IS NULL AND subject_id IS NOT NULL)
        OR (scope_kind = 'global' AND organization_id IS NULL AND subject_id IS NULL)
    ),
    CONSTRAINT job_messages_queue_check
        CHECK (queue ~ '^[a-z][a-z0-9_.-]{0,127}$'),
    CONSTRAINT job_messages_topic_check
        CHECK (topic ~ '^[a-z][a-z0-9_.-]{0,254}$'),
    CONSTRAINT job_messages_source_check
        CHECK (source ~ '^[a-z][a-z0-9_.:/-]{0,254}$'),
    CONSTRAINT job_messages_idempotency_key_check
        CHECK (length(idempotency_key) BETWEEN 1 AND 255),
    CONSTRAINT job_messages_request_fingerprint_check
        CHECK (octet_length(request_fingerprint) = 32),
    CONSTRAINT job_messages_ordering_key_check
        CHECK (ordering_key IS NULL OR length(ordering_key) BETWEEN 1 AND 255),
    CONSTRAINT job_messages_schema_version_check
        CHECK (schema_version > 0),
    CONSTRAINT job_messages_payload_size_check
        CHECK (octet_length(payload) <= 1048576),
    CONSTRAINT job_messages_content_type_check
        CHECK (length(content_type) BETWEEN 1 AND 255),
    CONSTRAINT job_messages_attributes_check
        CHECK (jsonb_typeof(attributes) = 'object' AND octet_length(attributes::text) <= 65536),
    CONSTRAINT job_messages_state_check CHECK (
        state IN ('pending', 'processing', 'retrying', 'succeeded', 'dead_letter', 'canceled')
    ),
    CONSTRAINT job_messages_priority_check
        CHECK (priority BETWEEN -100 AND 100),
    CONSTRAINT job_messages_attempts_check CHECK (
        attempt_count >= 0
        AND max_attempts BETWEEN 1 AND 100
        AND attempt_count <= max_attempts
        AND (state NOT IN ('processing', 'retrying', 'succeeded', 'dead_letter') OR attempt_count > 0)
        AND (state <> 'pending' OR attempt_count = 0)
        AND (state <> 'retrying' OR attempt_count < max_attempts)
    ),
    CONSTRAINT job_messages_lease_check CHECK (
        (
            state = 'processing'
            AND lease_owner IS NOT NULL
            AND length(lease_owner) BETWEEN 1 AND 255
            AND lease_token IS NOT NULL
            AND lease_expires_at IS NOT NULL
            AND heartbeat_at IS NOT NULL
        ) OR (
            state <> 'processing'
            AND lease_owner IS NULL
            AND lease_token IS NULL
            AND lease_expires_at IS NULL
            AND heartbeat_at IS NULL
        )
    ),
    CONSTRAINT job_messages_failure_check CHECK (
        (last_error_code IS NULL OR last_error_code ~ '^[a-z][a-z0-9_.-]{0,127}$')
        AND (last_error_message IS NULL OR length(last_error_message) <= 4096)
        AND (last_error_message IS NULL OR last_error_code IS NOT NULL)
        AND (state NOT IN ('retrying', 'dead_letter') OR last_error_code IS NOT NULL)
    ),
    CONSTRAINT job_messages_terminal_timestamps_check CHECK (
        ((state IN ('succeeded', 'canceled')) = (completed_at IS NOT NULL))
        AND ((state = 'dead_letter') = (dead_lettered_at IS NOT NULL))
    ),
    CONSTRAINT job_messages_state_version_check
        CHECK (state_version >= 0),
    CONSTRAINT job_messages_replay_check
        CHECK (replay_of IS NULL OR replay_of <> id)
);

CREATE UNIQUE INDEX uq_job_messages_idempotency
    ON public.job_messages (
        direction,
        scope_kind,
        COALESCE(organization_id::text, ''),
        COALESCE(subject_id::text, ''),
        queue,
        source,
        idempotency_key
    );

CREATE INDEX idx_job_messages_ready
    ON public.job_messages(queue, priority DESC, available_at, created_at, id)
    WHERE state IN ('pending', 'retrying');

CREATE INDEX idx_job_messages_expired_lease
    ON public.job_messages(lease_expires_at, queue, id)
    WHERE state = 'processing';

CREATE INDEX idx_job_messages_ordering
    ON public.job_messages(queue, ordering_key, created_at, id)
    WHERE ordering_key IS NOT NULL AND state IN ('pending', 'processing', 'retrying');

CREATE INDEX idx_job_messages_tenant
    ON public.job_messages(organization_id, created_at DESC)
    WHERE organization_id IS NOT NULL;

CREATE INDEX idx_job_messages_subject
    ON public.job_messages(subject_id, created_at DESC)
    WHERE subject_id IS NOT NULL;

CREATE INDEX idx_job_messages_dead_letter
    ON public.job_messages(queue, dead_lettered_at DESC, id)
    WHERE state = 'dead_letter';

CREATE TABLE public.job_attempts (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    job_id UUID NOT NULL REFERENCES public.job_messages(id) ON DELETE CASCADE,
    attempt_number INTEGER NOT NULL,
    worker_id TEXT NOT NULL,
    lease_token UUID NOT NULL,
    outcome TEXT,
    error_code TEXT,
    error_message TEXT,
    started_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    heartbeat_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    finished_at TIMESTAMPTZ,
    CONSTRAINT job_attempts_number_check
        CHECK (attempt_number > 0),
    CONSTRAINT job_attempts_worker_check
        CHECK (length(worker_id) BETWEEN 1 AND 255),
    CONSTRAINT job_attempts_outcome_check CHECK (
        outcome IS NULL OR outcome IN (
            'succeeded', 'retryable_failure', 'permanent_failure',
            'lease_expired', 'canceled'
        )
    ),
    CONSTRAINT job_attempts_completion_check CHECK (
        (outcome IS NULL) = (finished_at IS NULL)
        AND (outcome IS NOT NULL OR (error_code IS NULL AND error_message IS NULL))
        AND (
            outcome NOT IN ('retryable_failure', 'permanent_failure', 'lease_expired')
            OR error_code IS NOT NULL
        )
    ),
    CONSTRAINT job_attempts_failure_check CHECK (
        (error_code IS NULL OR error_code ~ '^[a-z][a-z0-9_.-]{0,127}$')
        AND (error_message IS NULL OR length(error_message) <= 4096)
        AND (error_message IS NULL OR error_code IS NOT NULL)
    ),
    CONSTRAINT job_attempts_clock_check CHECK (
        heartbeat_at >= started_at
        AND (finished_at IS NULL OR finished_at >= started_at)
    ),
    UNIQUE (job_id, attempt_number),
    UNIQUE (job_id, lease_token)
);

CREATE INDEX idx_job_attempts_open
    ON public.job_attempts(heartbeat_at, job_id)
    WHERE finished_at IS NULL;

CREATE TABLE public.job_state_transitions (
    sequence BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    job_id UUID NOT NULL REFERENCES public.job_messages(id) ON DELETE CASCADE,
    from_state TEXT,
    to_state TEXT NOT NULL,
    state_version BIGINT NOT NULL,
    attempt_count INTEGER NOT NULL,
    actor TEXT NOT NULL,
    error_code TEXT,
    error_message TEXT,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT job_state_transitions_from_state_check CHECK (
        from_state IS NULL OR from_state IN (
            'pending', 'processing', 'retrying', 'succeeded', 'dead_letter', 'canceled'
        )
    ),
    CONSTRAINT job_state_transitions_to_state_check CHECK (
        to_state IN ('pending', 'processing', 'retrying', 'succeeded', 'dead_letter', 'canceled')
    ),
    CONSTRAINT job_state_transitions_version_check
        CHECK (state_version >= 0),
    CONSTRAINT job_state_transitions_attempt_check
        CHECK (attempt_count >= 0),
    CONSTRAINT job_state_transitions_actor_check
        CHECK (length(actor) BETWEEN 1 AND 255),
    CONSTRAINT job_state_transitions_failure_check CHECK (
        (error_code IS NULL OR error_code ~ '^[a-z][a-z0-9_.-]{0,127}$')
        AND (error_message IS NULL OR length(error_message) <= 4096)
        AND (error_message IS NULL OR error_code IS NOT NULL)
    ),
    UNIQUE (job_id, state_version)
);

CREATE INDEX idx_job_state_transitions_job
    ON public.job_state_transitions(job_id, sequence);

CREATE FUNCTION public.enforce_job_attempt_state()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $function$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.outcome IS NOT NULL OR NEW.finished_at IS NOT NULL
           OR NEW.error_code IS NOT NULL OR NEW.error_message IS NOT NULL THEN
            RAISE EXCEPTION 'new job attempts must start open without an outcome or failure'
                USING ERRCODE = 'check_violation';
        END IF;
        RETURN NEW;
    END IF;

    IF NEW.id IS DISTINCT FROM OLD.id
       OR NEW.job_id IS DISTINCT FROM OLD.job_id
       OR NEW.attempt_number IS DISTINCT FROM OLD.attempt_number
       OR NEW.worker_id IS DISTINCT FROM OLD.worker_id
       OR NEW.lease_token IS DISTINCT FROM OLD.lease_token
       OR NEW.started_at IS DISTINCT FROM OLD.started_at THEN
        RAISE EXCEPTION 'job attempt identity and lease are immutable'
            USING ERRCODE = 'check_violation';
    END IF;

    IF OLD.outcome IS NOT NULL THEN
        RAISE EXCEPTION 'completed job attempt % is immutable', OLD.id
            USING ERRCODE = 'check_violation';
    END IF;

    IF NEW.heartbeat_at < OLD.heartbeat_at THEN
        RAISE EXCEPTION 'job attempt heartbeat cannot move backwards'
            USING ERRCODE = 'check_violation';
    END IF;

    RETURN NEW;
END
$function$;

CREATE TRIGGER job_attempts_enforce_state
BEFORE INSERT OR UPDATE ON public.job_attempts
FOR EACH ROW EXECUTE FUNCTION public.enforce_job_attempt_state();

CREATE FUNCTION public.job_state_transition_is_valid(from_state TEXT, to_state TEXT)
RETURNS BOOLEAN
LANGUAGE sql
IMMUTABLE
STRICT
PARALLEL SAFE
SET search_path = pg_catalog
AS $function$
    SELECT CASE from_state
        WHEN 'pending' THEN to_state IN ('processing', 'canceled')
        WHEN 'processing' THEN to_state IN ('retrying', 'succeeded', 'dead_letter')
        WHEN 'retrying' THEN to_state IN ('processing', 'canceled')
        ELSE FALSE
    END
$function$;

CREATE FUNCTION public.enforce_job_message_state()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $function$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.state <> 'pending' OR NEW.state_version <> 0 OR NEW.attempt_count <> 0 THEN
            RAISE EXCEPTION 'new jobs must start pending at state version 0 with no attempts'
                USING ERRCODE = 'check_violation';
        END IF;
        RETURN NEW;
    END IF;

    IF NEW.direction IS DISTINCT FROM OLD.direction
       OR NEW.scope_kind IS DISTINCT FROM OLD.scope_kind
       OR NEW.organization_id IS DISTINCT FROM OLD.organization_id
       OR NEW.subject_id IS DISTINCT FROM OLD.subject_id
       OR NEW.queue IS DISTINCT FROM OLD.queue
       OR NEW.topic IS DISTINCT FROM OLD.topic
       OR NEW.source IS DISTINCT FROM OLD.source
       OR NEW.idempotency_key IS DISTINCT FROM OLD.idempotency_key
       OR NEW.request_fingerprint IS DISTINCT FROM OLD.request_fingerprint
       OR NEW.ordering_key IS DISTINCT FROM OLD.ordering_key
       OR NEW.schema_version IS DISTINCT FROM OLD.schema_version
       OR NEW.payload IS DISTINCT FROM OLD.payload
       OR NEW.content_type IS DISTINCT FROM OLD.content_type
       OR NEW.attributes IS DISTINCT FROM OLD.attributes
       OR NEW.max_attempts IS DISTINCT FROM OLD.max_attempts
       OR NEW.replay_of IS DISTINCT FROM OLD.replay_of
       OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'job identity, routing, scope, payload, and retry policy are immutable'
            USING ERRCODE = 'check_violation';
    END IF;

    IF OLD.state IN ('succeeded', 'dead_letter', 'canceled') THEN
        RAISE EXCEPTION 'terminal job % is immutable', OLD.id
            USING ERRCODE = 'check_violation';
    END IF;

    IF NEW.state IS DISTINCT FROM OLD.state THEN
        IF NOT public.job_state_transition_is_valid(OLD.state, NEW.state) THEN
            RAISE EXCEPTION 'invalid job state transition: % -> %', OLD.state, NEW.state
                USING ERRCODE = 'check_violation';
        END IF;
        IF NEW.state = 'processing' AND NEW.attempt_count <> OLD.attempt_count + 1 THEN
            RAISE EXCEPTION 'processing transition must increment attempt_count exactly once'
                USING ERRCODE = 'check_violation';
        END IF;
        IF NEW.state <> 'processing' AND NEW.attempt_count <> OLD.attempt_count THEN
            RAISE EXCEPTION 'attempt_count changes only when a job enters processing'
                USING ERRCODE = 'check_violation';
        END IF;
        NEW.state_version := OLD.state_version + 1;
    ELSIF NEW.state_version IS DISTINCT FROM OLD.state_version
       OR NEW.attempt_count IS DISTINCT FROM OLD.attempt_count THEN
        RAISE EXCEPTION 'state_version and attempt_count are state-machine owned'
            USING ERRCODE = 'check_violation';
    END IF;

    NEW.updated_at := CURRENT_TIMESTAMP;
    RETURN NEW;
END
$function$;

CREATE TRIGGER job_messages_enforce_state
BEFORE INSERT OR UPDATE ON public.job_messages
FOR EACH ROW EXECUTE FUNCTION public.enforce_job_message_state();

CREATE FUNCTION public.append_job_state_transition()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $function$
DECLARE
    transition_actor TEXT;
BEGIN
    IF TG_OP = 'UPDATE' AND NEW.state IS NOT DISTINCT FROM OLD.state THEN
        RETURN NEW;
    END IF;

    transition_actor := COALESCE(
        NULLIF(current_setting('role', true), 'none'),
        session_user::text
    );

    INSERT INTO public.job_state_transitions (
        job_id,
        from_state,
        to_state,
        state_version,
        attempt_count,
        actor,
        error_code,
        error_message
    ) VALUES (
        NEW.id,
        CASE WHEN TG_OP = 'INSERT' THEN NULL ELSE OLD.state END,
        NEW.state,
        NEW.state_version,
        NEW.attempt_count,
        transition_actor,
        NEW.last_error_code,
        NEW.last_error_message
    );
    RETURN NEW;
END
$function$;

REVOKE ALL ON FUNCTION public.job_state_transition_is_valid(TEXT, TEXT) FROM PUBLIC;
REVOKE ALL ON FUNCTION public.enforce_job_attempt_state() FROM PUBLIC;
REVOKE ALL ON FUNCTION public.enforce_job_message_state() FROM PUBLIC;
REVOKE ALL ON FUNCTION public.append_job_state_transition() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.job_state_transition_is_valid(TEXT, TEXT) TO app_job_worker;

CREATE TRIGGER job_messages_append_transition
AFTER INSERT OR UPDATE OF state ON public.job_messages
FOR EACH ROW EXECUTE FUNCTION public.append_job_state_transition();

-- enqueue_job_message is the only request-traffic append capability. It keeps
-- idempotency resolution inside the database without granting SELECT or UPDATE
-- on payload-bearing job rows to app_tenant. The caller role and signed scope
-- settings are checked explicitly because SECURITY DEFINER bypasses table RLS.
CREATE FUNCTION public.enqueue_job_message(
    p_direction TEXT,
    p_scope_kind TEXT,
    p_organization_id UUID,
    p_subject_id UUID,
    p_queue TEXT,
    p_topic TEXT,
    p_source TEXT,
    p_idempotency_key TEXT,
    p_ordering_key TEXT,
    p_schema_version INTEGER,
    p_payload BYTEA,
    p_content_type TEXT,
    p_attributes JSONB,
    p_priority SMALLINT,
    p_max_attempts INTEGER,
    p_available_at TIMESTAMPTZ,
    p_request_fingerprint BYTEA
)
RETURNS TABLE(job_id UUID, stored_fingerprint BYTEA, inserted BOOLEAN)
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
        IF p_direction <> 'outbox' THEN
            RAISE EXCEPTION 'request traffic may enqueue outbox work only'
                USING ERRCODE = 'insufficient_privilege';
        END IF;
        IF NOT (
            (
                p_scope_kind = 'tenant'
                AND p_organization_id = NULLIF(
                    current_setting('app.current_org_id', true), ''
                )::uuid
                AND p_subject_id IS NULL
            )
            OR (
                p_scope_kind = 'subject'
                AND p_subject_id = NULLIF(
                    current_setting('app.current_user_id', true), ''
                )::uuid
                AND p_organization_id IS NULL
            )
        ) THEN
            RAISE EXCEPTION 'job scope does not match the signed request scope'
                USING ERRCODE = 'insufficient_privilege';
        END IF;
    ELSIF caller_role = 'app_control_plane' THEN
        IF p_direction <> 'outbox'
           OR p_scope_kind <> 'global'
           OR p_organization_id IS NOT NULL
           OR p_subject_id IS NOT NULL THEN
            RAISE EXCEPTION 'control-plane traffic may enqueue global outbox work only'
                USING ERRCODE = 'insufficient_privilege';
        END IF;
    ELSIF caller_role <> 'app_job_worker' THEN
        RAISE EXCEPTION 'role % cannot enqueue jobs', caller_role
            USING ERRCODE = 'insufficient_privilege';
    END IF;

    INSERT INTO public.job_messages (
        direction,
        scope_kind,
        organization_id,
        subject_id,
        queue,
        topic,
        source,
        idempotency_key,
        request_fingerprint,
        ordering_key,
        schema_version,
        payload,
        content_type,
        attributes,
        priority,
        max_attempts,
        available_at
    ) VALUES (
        p_direction,
        p_scope_kind,
        p_organization_id,
        p_subject_id,
        p_queue,
        p_topic,
        p_source,
        p_idempotency_key,
        p_request_fingerprint,
        p_ordering_key,
        p_schema_version,
        p_payload,
        p_content_type,
        p_attributes,
        p_priority,
        p_max_attempts,
        COALESCE(p_available_at, CURRENT_TIMESTAMP)
    )
    ON CONFLICT DO NOTHING
    RETURNING id INTO inserted_id;

    IF inserted_id IS NOT NULL THEN
        RETURN QUERY SELECT inserted_id, p_request_fingerprint, TRUE;
        RETURN;
    END IF;

    RETURN QUERY
    SELECT message.id, message.request_fingerprint, FALSE
    FROM public.job_messages AS message
    WHERE message.direction = p_direction
      AND message.scope_kind = p_scope_kind
      AND message.organization_id IS NOT DISTINCT FROM p_organization_id
      AND message.subject_id IS NOT DISTINCT FROM p_subject_id
      AND message.queue = p_queue
      AND message.source = p_source
      AND message.idempotency_key = p_idempotency_key;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'job idempotency conflict could not be resolved'
            USING ERRCODE = 'serialization_failure';
    END IF;
END
$function$;

REVOKE ALL ON FUNCTION public.enqueue_job_message(
    TEXT, TEXT, UUID, UUID, TEXT, TEXT, TEXT, TEXT, TEXT, INTEGER,
    BYTEA, TEXT, JSONB, SMALLINT, INTEGER, TIMESTAMPTZ, BYTEA
) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.enqueue_job_message(
    TEXT, TEXT, UUID, UUID, TEXT, TEXT, TEXT, TEXT, TEXT, INTEGER,
    BYTEA, TEXT, JSONB, SMALLINT, INTEGER, TIMESTAMPTZ, BYTEA
) TO app_tenant, app_control_plane, app_job_worker;

-- Replay is a privileged copy operation, never an update of terminal history.
-- The function copies the source payload entirely inside PostgreSQL so neither
-- the platform-admin API nor a browser ever receives payload bytes.
CREATE FUNCTION public.replay_job_message(
    p_source_job_id UUID,
    p_idempotency_key TEXT,
    p_available_at TIMESTAMPTZ,
    p_request_fingerprint BYTEA
)
RETURNS TABLE(job_id UUID, stored_fingerprint BYTEA, inserted BOOLEAN)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $function$
DECLARE
    caller_role TEXT;
    source_message public.job_messages%ROWTYPE;
    inserted_id UUID;
BEGIN
    caller_role := COALESCE(
        NULLIF(current_setting('role', true), 'none'),
        session_user::text
    );
    IF caller_role <> 'app_job_worker' THEN
        RAISE EXCEPTION 'role % cannot replay jobs', caller_role
            USING ERRCODE = 'insufficient_privilege';
    END IF;

    SELECT * INTO source_message
    FROM public.job_messages
    WHERE id = p_source_job_id;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'source job does not exist'
            USING ERRCODE = 'no_data_found';
    END IF;
    IF source_message.state <> 'dead_letter' THEN
        RAISE EXCEPTION 'only dead-lettered jobs may be replayed'
            USING ERRCODE = 'object_not_in_prerequisite_state';
    END IF;

    INSERT INTO public.job_messages (
        direction,
        scope_kind,
        organization_id,
        subject_id,
        queue,
        topic,
        source,
        idempotency_key,
        request_fingerprint,
        ordering_key,
        schema_version,
        payload,
        content_type,
        attributes,
        priority,
        max_attempts,
        available_at,
        replay_of
    ) VALUES (
        source_message.direction,
        source_message.scope_kind,
        source_message.organization_id,
        source_message.subject_id,
        source_message.queue,
        source_message.topic,
        source_message.source,
        p_idempotency_key,
        p_request_fingerprint,
        source_message.ordering_key,
        source_message.schema_version,
        source_message.payload,
        source_message.content_type,
        source_message.attributes,
        source_message.priority,
        source_message.max_attempts,
        COALESCE(p_available_at, CURRENT_TIMESTAMP),
        source_message.id
    )
    ON CONFLICT DO NOTHING
    RETURNING id INTO inserted_id;

    IF inserted_id IS NOT NULL THEN
        RETURN QUERY SELECT inserted_id, p_request_fingerprint, TRUE;
        RETURN;
    END IF;

    RETURN QUERY
    SELECT message.id, message.request_fingerprint, FALSE
    FROM public.job_messages AS message
    WHERE message.direction = source_message.direction
      AND message.scope_kind = source_message.scope_kind
      AND message.organization_id IS NOT DISTINCT FROM source_message.organization_id
      AND message.subject_id IS NOT DISTINCT FROM source_message.subject_id
      AND message.queue = source_message.queue
      AND message.source = source_message.source
      AND message.idempotency_key = p_idempotency_key;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'job replay idempotency conflict could not be resolved'
            USING ERRCODE = 'serialization_failure';
    END IF;
END
$function$;

REVOKE ALL ON FUNCTION public.replay_job_message(UUID, TEXT, TIMESTAMPTZ, BYTEA)
FROM PUBLIC, app_tenant, app_control_plane, app_billing_worker, app_webhook_worker;
GRANT EXECUTE ON FUNCTION public.replay_job_message(UUID, TEXT, TIMESTAMPTZ, BYTEA)
TO app_job_worker;

ALTER TABLE public.job_messages ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.job_messages FORCE ROW LEVEL SECURITY;

CREATE POLICY job_messages_request_enqueue
ON public.job_messages
FOR INSERT
TO app_tenant
WITH CHECK (
    direction = 'outbox'
    AND replay_of IS NULL
    AND (
        (
            scope_kind = 'tenant'
            AND organization_id = NULLIF(current_setting('app.current_org_id', true), '')::uuid
        )
        OR (
            scope_kind = 'subject'
            AND subject_id = NULLIF(current_setting('app.current_user_id', true), '')::uuid
        )
    )
);

REVOKE ALL PRIVILEGES ON
    public.job_messages,
    public.job_attempts,
    public.job_state_transitions
FROM PUBLIC, app_tenant, app_control_plane, app_billing_worker, app_webhook_worker, app_job_worker;

GRANT SELECT, INSERT, UPDATE ON
    public.job_messages,
    public.job_attempts
TO app_job_worker;

GRANT SELECT ON public.job_state_transitions TO app_job_worker;

REVOKE ALL ON SEQUENCE public.job_state_transitions_sequence_seq
FROM PUBLIC, app_tenant, app_control_plane, app_billing_worker, app_webhook_worker, app_job_worker;
