-- Migration 75: durable email outbox convergence.
--
-- Pre-authentication commands have no tenant or subject identity. Permit the
-- audited control-plane transaction to append global outbox work, while still
-- denying inbox receipt, tenant/subject spoofing, payload reads, and every job
-- lifecycle operation.

CREATE OR REPLACE FUNCTION public.enqueue_job_message(
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

INSERT INTO email_templates (
    id, name, subject_template, html_template, text_template
) VALUES (
    '00000000-0000-0000-0000-000000000075',
    'magic_link',
    'Your sign-in link',
    '<h1>Sign in to your account</h1><p><a href="{{link_url}}">Sign in</a>. This link expires in 15 minutes.</p>',
    'Sign in to your account (expires in 15 minutes): {{link_url}}'
)
ON CONFLICT (name) DO NOTHING;
