-- publish_domain_event admits one tenant-less event from a tenant-scoped caller:
-- the journal record of an audit entry that has no organization, written by a
-- user-scoped transaction for that user's own action.
--
-- Under the warehouse audit backend every audit entry is published to the
-- journal as the internal type saas.audit.recorded.v1 in the caller's
-- transaction (ADR 0009). A user-scoped transaction runs as app_tenant and sets
-- only app.current_user_id, and the function refused every NULL tenant from
-- app_tenant, so an entry with no organization written there (a WebAuthn
-- registration, an MFA enrolment) could not be published. audit_events already
-- admits exactly that row (policy audit_events_tenant, WITH CHECK: org_id IS NULL
-- AND actor_id equal to app.current_user_id); the journal now admits the same
-- one, and nothing wider. The app_tenant branch admits a NULL tenant only when
-- the type is saas.audit.recorded.v1, app.current_user_id is set and non-empty,
-- and p_actor_principal_id is that user. A person's principal id is their user
-- id (sync_human_principal), and the emitter sets the envelope's actor to the
-- audit entry's actor id, the value audit_events.actor_id records.
--
-- Every other refusal, the advisory lock, the insert and the return are the
-- function's own, unchanged. CREATE OR REPLACE keeps the owner and the grants and
-- replaces every attribute, so SECURITY DEFINER and the search_path migration 12
-- set are stated again. Replacing a function needs its owner's authority and
-- CREATE on the schema: the migration principal grants the owner CREATE for the
-- replacement and takes it back, assumes the owner for it, and hands the
-- transaction back before the ledger write, as migrations 12 and 35 do.

GRANT CREATE ON SCHEMA public TO app_control_plane;
SET LOCAL ROLE app_control_plane;

CREATE OR REPLACE FUNCTION public.publish_domain_event(p_id uuid, p_type text, p_source text, p_subject text, p_event_time timestamp with time zone, p_specversion text, p_datacontenttype text, p_dataschema text, p_data bytea, p_tenant_id uuid, p_boundary_id text, p_partition_key text, p_correlation_id text, p_causation_id text, p_actor_principal_id text, p_owner_principal_id text, p_traceparent text, p_schema_version integer, p_request_fingerprint bytea) RETURNS TABLE(event_id uuid, stored_fingerprint bytea, inserted boolean)
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $$
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
            -- The one tenant-less event a tenant-scoped caller may publish: the
            -- journal record of an audit entry that has no organization, written
            -- by a user-scoped transaction for that user's own action. It
            -- publishes for no tenant, and only the internal audit type
            -- saas.audit.recorded.v1 may do so. The rule mirrors the
            -- audit_events_tenant policy that admits the same row into
            -- audit_events (org_id IS NULL and actor_id equal to
            -- app.current_user_id): app.current_user_id must be set and
            -- non-empty, and the event's actor must be that user. The type is
            -- internal, so the relay never fans it out and no subscription can
            -- match it. Anything else that reaches here is refused: a tenant that
            -- is not the request's, and a tenant-less event of any other type or
            -- for any other actor.
            IF p_tenant_id IS NOT NULL
               OR p_type IS DISTINCT FROM 'saas.audit.recorded.v1'
               OR NULLIF(current_setting('app.current_user_id', true), '') IS NULL
               OR p_actor_principal_id IS DISTINCT FROM NULLIF(current_setting('app.current_user_id', true), '') THEN
                RAISE EXCEPTION 'event tenant does not match the signed request scope'
                    USING ERRCODE = 'insufficient_privilege';
            END IF;
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
$$;

RESET ROLE;
REVOKE CREATE ON SCHEMA public FROM app_control_plane;
