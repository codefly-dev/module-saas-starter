-- Explicit background row visibility. Existing SQL ACLs remain unchanged.
-- Exact current_user prevents inherited role membership from widening a tenant role.
CREATE POLICY app_billing_worker_explicit_rows ON public.organizations FOR ALL TO app_billing_worker
 USING (current_user = 'app_billing_worker') WITH CHECK (current_user = 'app_billing_worker');
CREATE POLICY app_billing_worker_explicit_rows ON public.subscriptions FOR ALL TO app_billing_worker
 USING (current_user = 'app_billing_worker') WITH CHECK (current_user = 'app_billing_worker');
CREATE POLICY app_billing_worker_explicit_rows ON public.users FOR ALL TO app_billing_worker
 USING (current_user = 'app_billing_worker') WITH CHECK (current_user = 'app_billing_worker');
CREATE POLICY app_control_plane_explicit_rows ON public.actor_chain_journal FOR ALL TO app_control_plane
 USING (current_user = 'app_control_plane') WITH CHECK (current_user = 'app_control_plane');
CREATE POLICY app_control_plane_explicit_rows ON public.actor_chain_revocations FOR ALL TO app_control_plane
 USING (current_user = 'app_control_plane') WITH CHECK (current_user = 'app_control_plane');
CREATE POLICY app_control_plane_explicit_rows ON public.api_keys FOR ALL TO app_control_plane
 USING (current_user = 'app_control_plane') WITH CHECK (current_user = 'app_control_plane');
CREATE POLICY app_control_plane_explicit_rows ON public.approval_decisions FOR ALL TO app_control_plane
 USING (current_user = 'app_control_plane') WITH CHECK (current_user = 'app_control_plane');
CREATE POLICY app_control_plane_explicit_rows ON public.approval_requests FOR ALL TO app_control_plane
 USING (current_user = 'app_control_plane') WITH CHECK (current_user = 'app_control_plane');
CREATE POLICY app_control_plane_explicit_rows ON public.audit_event_idempotency FOR ALL TO app_control_plane
 USING (current_user = 'app_control_plane') WITH CHECK (current_user = 'app_control_plane');
CREATE POLICY app_control_plane_explicit_rows ON public.audit_events FOR ALL TO app_control_plane
 USING (current_user = 'app_control_plane') WITH CHECK (current_user = 'app_control_plane');
CREATE POLICY app_control_plane_explicit_rows ON public.connector_credentials FOR ALL TO app_control_plane
 USING (current_user = 'app_control_plane') WITH CHECK (current_user = 'app_control_plane');
CREATE POLICY app_control_plane_explicit_rows ON public.dashboards FOR ALL TO app_control_plane
 USING (current_user = 'app_control_plane') WITH CHECK (current_user = 'app_control_plane');
CREATE POLICY app_control_plane_explicit_rows ON public.datasource_sources FOR ALL TO app_control_plane
 USING (current_user = 'app_control_plane') WITH CHECK (current_user = 'app_control_plane');
CREATE POLICY app_control_plane_explicit_rows ON public.delegation_grants FOR ALL TO app_control_plane
 USING (current_user = 'app_control_plane') WITH CHECK (current_user = 'app_control_plane');
CREATE POLICY app_control_plane_explicit_rows ON public.domain_events FOR ALL TO app_control_plane
 USING (current_user = 'app_control_plane') WITH CHECK (current_user = 'app_control_plane');
CREATE POLICY app_control_plane_explicit_rows ON public.entitlement_overrides FOR ALL TO app_control_plane
 USING (current_user = 'app_control_plane') WITH CHECK (current_user = 'app_control_plane');
CREATE POLICY app_control_plane_explicit_rows ON public.execution_custody FOR ALL TO app_control_plane
 USING (current_user = 'app_control_plane') WITH CHECK (current_user = 'app_control_plane');
CREATE POLICY app_control_plane_explicit_rows ON public.gdpr_requests FOR ALL TO app_control_plane
 USING (current_user = 'app_control_plane') WITH CHECK (current_user = 'app_control_plane');
CREATE POLICY app_control_plane_explicit_rows ON public.installations FOR ALL TO app_control_plane
 USING (current_user = 'app_control_plane') WITH CHECK (current_user = 'app_control_plane');
CREATE POLICY app_control_plane_explicit_rows ON public.invitations FOR ALL TO app_control_plane
 USING (current_user = 'app_control_plane') WITH CHECK (current_user = 'app_control_plane');
CREATE POLICY app_control_plane_explicit_rows ON public.magic_links FOR ALL TO app_control_plane
 USING (current_user = 'app_control_plane') WITH CHECK (current_user = 'app_control_plane');
CREATE POLICY app_control_plane_explicit_rows ON public.membership_integrity_findings FOR ALL TO app_control_plane
 USING (current_user = 'app_control_plane') WITH CHECK (current_user = 'app_control_plane');
CREATE POLICY app_control_plane_explicit_rows ON public.mfa_backup_codes FOR ALL TO app_control_plane
 USING (current_user = 'app_control_plane') WITH CHECK (current_user = 'app_control_plane');
CREATE POLICY app_control_plane_explicit_rows ON public.mfa_devices FOR ALL TO app_control_plane
 USING (current_user = 'app_control_plane') WITH CHECK (current_user = 'app_control_plane');
CREATE POLICY app_control_plane_explicit_rows ON public.mfa_login_transactions FOR ALL TO app_control_plane
 USING (current_user = 'app_control_plane') WITH CHECK (current_user = 'app_control_plane');
CREATE POLICY app_control_plane_explicit_rows ON public.notifications FOR ALL TO app_control_plane
 USING (current_user = 'app_control_plane') WITH CHECK (current_user = 'app_control_plane');
CREATE POLICY app_control_plane_explicit_rows ON public.onboarding_progress FOR ALL TO app_control_plane
 USING (current_user = 'app_control_plane') WITH CHECK (current_user = 'app_control_plane');
CREATE POLICY app_control_plane_explicit_rows ON public.org_generic_settings FOR ALL TO app_control_plane
 USING (current_user = 'app_control_plane') WITH CHECK (current_user = 'app_control_plane');
CREATE POLICY app_control_plane_explicit_rows ON public.org_identity_providers FOR ALL TO app_control_plane
 USING (current_user = 'app_control_plane') WITH CHECK (current_user = 'app_control_plane');
CREATE POLICY app_control_plane_explicit_rows ON public.org_settings FOR ALL TO app_control_plane
 USING (current_user = 'app_control_plane') WITH CHECK (current_user = 'app_control_plane');
CREATE POLICY app_control_plane_explicit_rows ON public.organization_activations FOR ALL TO app_control_plane
 USING (current_user = 'app_control_plane') WITH CHECK (current_user = 'app_control_plane');
CREATE POLICY app_control_plane_explicit_rows ON public.organization_authorization_revisions FOR ALL TO app_control_plane
 USING (current_user = 'app_control_plane') WITH CHECK (current_user = 'app_control_plane');
CREATE POLICY app_control_plane_explicit_rows ON public.organization_members FOR ALL TO app_control_plane
 USING (current_user = 'app_control_plane') WITH CHECK (current_user = 'app_control_plane');
CREATE POLICY app_control_plane_explicit_rows ON public.organizations FOR ALL TO app_control_plane
 USING (current_user = 'app_control_plane') WITH CHECK (current_user = 'app_control_plane');
CREATE POLICY app_control_plane_explicit_rows ON public.principal_authorization_revisions FOR ALL TO app_control_plane
 USING (current_user = 'app_control_plane') WITH CHECK (current_user = 'app_control_plane');
CREATE POLICY app_control_plane_explicit_rows ON public.principals FOR ALL TO app_control_plane
 USING (current_user = 'app_control_plane') WITH CHECK (current_user = 'app_control_plane');
CREATE POLICY app_control_plane_explicit_rows ON public.record_shares FOR ALL TO app_control_plane
 USING (current_user = 'app_control_plane') WITH CHECK (current_user = 'app_control_plane');
CREATE POLICY app_control_plane_explicit_rows ON public.role_assignments FOR ALL TO app_control_plane
 USING (current_user = 'app_control_plane') WITH CHECK (current_user = 'app_control_plane');
CREATE POLICY app_control_plane_explicit_rows ON public.role_permissions FOR ALL TO app_control_plane
 USING (current_user = 'app_control_plane') WITH CHECK (current_user = 'app_control_plane');
CREATE POLICY app_control_plane_explicit_rows ON public.roles FOR ALL TO app_control_plane
 USING (current_user = 'app_control_plane') WITH CHECK (current_user = 'app_control_plane');
CREATE POLICY app_control_plane_explicit_rows ON public.scope_grants FOR ALL TO app_control_plane
 USING (current_user = 'app_control_plane') WITH CHECK (current_user = 'app_control_plane');
CREATE POLICY app_control_plane_explicit_rows ON public.scope_nodes FOR ALL TO app_control_plane
 USING (current_user = 'app_control_plane') WITH CHECK (current_user = 'app_control_plane');
CREATE POLICY app_control_plane_explicit_rows ON public.sessions FOR ALL TO app_control_plane
 USING (current_user = 'app_control_plane') WITH CHECK (current_user = 'app_control_plane');
CREATE POLICY app_control_plane_explicit_rows ON public.subscriptions FOR ALL TO app_control_plane
 USING (current_user = 'app_control_plane') WITH CHECK (current_user = 'app_control_plane');
CREATE POLICY app_control_plane_explicit_rows ON public.team_members FOR ALL TO app_control_plane
 USING (current_user = 'app_control_plane') WITH CHECK (current_user = 'app_control_plane');
CREATE POLICY app_control_plane_explicit_rows ON public.team_membership_quarantine FOR ALL TO app_control_plane
 USING (current_user = 'app_control_plane') WITH CHECK (current_user = 'app_control_plane');
CREATE POLICY app_control_plane_explicit_rows ON public.teams FOR ALL TO app_control_plane
 USING (current_user = 'app_control_plane') WITH CHECK (current_user = 'app_control_plane');
CREATE POLICY app_control_plane_explicit_rows ON public.usage_events FOR ALL TO app_control_plane
 USING (current_user = 'app_control_plane') WITH CHECK (current_user = 'app_control_plane');
CREATE POLICY app_control_plane_explicit_rows ON public.usage_totals FOR ALL TO app_control_plane
 USING (current_user = 'app_control_plane') WITH CHECK (current_user = 'app_control_plane');
CREATE POLICY app_control_plane_explicit_rows ON public.user_consent_events FOR ALL TO app_control_plane
 USING (current_user = 'app_control_plane') WITH CHECK (current_user = 'app_control_plane');
CREATE POLICY app_control_plane_explicit_rows ON public.user_consent_preferences FOR ALL TO app_control_plane
 USING (current_user = 'app_control_plane') WITH CHECK (current_user = 'app_control_plane');
CREATE POLICY app_control_plane_explicit_rows ON public.user_identities FOR ALL TO app_control_plane
 USING (current_user = 'app_control_plane') WITH CHECK (current_user = 'app_control_plane');
CREATE POLICY app_control_plane_explicit_rows ON public.users FOR ALL TO app_control_plane
 USING (current_user = 'app_control_plane') WITH CHECK (current_user = 'app_control_plane');
CREATE POLICY app_control_plane_explicit_rows ON public.waitlist_entries FOR ALL TO app_control_plane
 USING (current_user = 'app_control_plane') WITH CHECK (current_user = 'app_control_plane');
CREATE POLICY app_control_plane_explicit_rows ON public.webauthn_ceremonies FOR ALL TO app_control_plane
 USING (current_user = 'app_control_plane') WITH CHECK (current_user = 'app_control_plane');
CREATE POLICY app_control_plane_explicit_rows ON public.webauthn_credentials FOR ALL TO app_control_plane
 USING (current_user = 'app_control_plane') WITH CHECK (current_user = 'app_control_plane');
CREATE POLICY app_control_plane_explicit_rows ON public.webhook_deliveries FOR ALL TO app_control_plane
 USING (current_user = 'app_control_plane') WITH CHECK (current_user = 'app_control_plane');
CREATE POLICY app_control_plane_explicit_rows ON public.webhook_subscriptions FOR ALL TO app_control_plane
 USING (current_user = 'app_control_plane') WITH CHECK (current_user = 'app_control_plane');
CREATE POLICY app_control_plane_explicit_rows ON public.work_context_replay FOR ALL TO app_control_plane
 USING (current_user = 'app_control_plane') WITH CHECK (current_user = 'app_control_plane');
CREATE POLICY app_job_worker_explicit_rows ON public.approval_requests FOR ALL TO app_job_worker
 USING (current_user = 'app_job_worker') WITH CHECK (current_user = 'app_job_worker');
CREATE POLICY app_job_worker_explicit_rows ON public.domain_events FOR ALL TO app_job_worker
 USING (current_user = 'app_job_worker') WITH CHECK (current_user = 'app_job_worker');
CREATE POLICY app_job_worker_explicit_rows ON public.job_messages FOR ALL TO app_job_worker
 USING (current_user = 'app_job_worker') WITH CHECK (current_user = 'app_job_worker');
CREATE POLICY app_job_worker_explicit_rows ON public.webhook_deliveries FOR ALL TO app_job_worker
 USING (current_user = 'app_job_worker') WITH CHECK (current_user = 'app_job_worker');
CREATE POLICY app_job_worker_explicit_rows ON public.webhook_subscriptions FOR ALL TO app_job_worker
 USING (current_user = 'app_job_worker') WITH CHECK (current_user = 'app_job_worker');
CREATE POLICY app_webhook_worker_explicit_rows ON public.webhook_deliveries FOR ALL TO app_webhook_worker
 USING (current_user = 'app_webhook_worker') WITH CHECK (current_user = 'app_webhook_worker');
CREATE POLICY app_webhook_worker_explicit_rows ON public.webhook_subscriptions FOR ALL TO app_webhook_worker
 USING (current_user = 'app_webhook_worker') WITH CHECK (current_user = 'app_webhook_worker');

-- Historical default grants could include the migration engine's ledger.
-- Runtime roles must never edit the record of installed schema versions.
DO $ledger$ BEGIN
 IF to_regclass('public.schema_migrations') IS NOT NULL THEN
  REVOKE ALL ON public.schema_migrations FROM PUBLIC, app_tenant,
   app_control_plane, app_billing_worker, app_webhook_worker, app_job_worker;
 END IF;
END $ledger$;

-- Missing or empty request scope is denial, including SQL NULL predicates.
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
        IF (
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
        ) IS NOT TRUE THEN
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

-- Preserve guarded function-only access without a privileged schema-owner session.
GRANT CREATE ON SCHEMA public TO app_control_plane;
ALTER FUNCTION public.publish_domain_event(uuid,text,text,text,timestamp with time zone,text,text,text,bytea,uuid,text,text,text,text,text,text,text,integer,bytea) OWNER TO app_control_plane;
REVOKE CREATE ON SCHEMA public FROM app_control_plane;
GRANT CREATE ON SCHEMA public TO app_job_worker;
ALTER FUNCTION public.enqueue_job_message(text,text,uuid,uuid,text,text,text,text,text,integer,bytea,text,jsonb,smallint,integer,timestamp with time zone,bytea) OWNER TO app_job_worker;
ALTER FUNCTION public.replay_job_message(uuid,text,timestamp with time zone,bytea) OWNER TO app_job_worker;
REVOKE CREATE ON SCHEMA public FROM app_job_worker;

-- Subscription sync retains its schema-owner authority to DELETE subscription
-- bindings. No runtime role receives that table privilege. Its guarded lookup
-- needs SELECT visibility on the FORCE-RLS endpoint table without BYPASSRLS.
DO $owner_policy$ DECLARE owner_role name; BEGIN
 SELECT pg_get_userbyid(proowner) INTO STRICT owner_role FROM pg_proc
 WHERE oid='public.sync_webhook_event_subscriptions(uuid,uuid,text[])'::regprocedure;
 EXECUTE format('CREATE POLICY webhook_subscriptions_migration_owner_read ON public.webhook_subscriptions FOR SELECT TO %I USING (current_user = %L)', owner_role, owner_role);
END $owner_policy$;

-- The named control plane now spans rows through explicit policies.
CREATE OR REPLACE FUNCTION public.record_membership_integrity_findings()
RETURNS integer
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $function$
DECLARE
    outstanding integer;
BEGIN
    -- An inventory has to see every organization, and organizations,
    -- organization_members and users (administrator eligibility depends on
    -- identity status) are all FORCE ROW LEVEL SECURITY — which subjects the
    -- table owner to their policies as well. Those policies scope to
    -- app.current_org_id and carry no bypass clause (a session setting must
    -- never grant RLS authority; the role-hardening suite asserts no live
    -- policy reads one). A caller that does not span organizations therefore
    -- reads zero rows, records nothing, and returns 0 — a result identical to a
    -- healthy platform and one nobody would think to question. Refuse instead.
    IF current_user <> 'app_control_plane' THEN
        RAISE EXCEPTION
            'record_membership_integrity_findings() must run as a role that spans every organization; % does not, and would record an empty backlog',
            current_user
            USING HINT = 'assume the control plane first: SET ROLE app_control_plane';
    END IF;

    WITH org_administration AS (
        -- One pass over the membership graph, so every finding below is decided
        -- from the same per-organization facts rather than from repeated
        -- correlated subqueries that could drift apart.
        --
        -- "Eligible" is migration 133's definition, deliberately reproduced
        -- rather than approximated: role IN ('owner','admin') AND the identity
        -- is active. findIdentity admits only active identities and DeleteUser
        -- is a soft delete that leaves the membership row standing, so counting
        -- every administrative row regardless of identity status reports an
        -- organization healthy when nobody can actually sign in and administer
        -- it. That organization is precisely the backlog this inventory exists
        -- to size. Migration 133's organization_eligible_administrators() is
        -- not reused here: it is SECURITY DEFINER scoped to app.current_org_id,
        -- so it answers for one tenant, and this is a cross-tenant inventory.
        SELECT
            o.id       AS org_id,
            o.owner_id AS owner_id,
            (
                SELECT om.role FROM organization_members om
                WHERE om.org_id = o.id AND om.user_id = o.owner_id
            ) AS owner_membership_role,
            (
                SELECT u.status::text FROM users u WHERE u.uuid = o.owner_id
            ) AS owner_status,
            count(*) FILTER (
                WHERE member.role IN ('owner', 'admin')
            ) AS administrative_members,
            count(*) FILTER (
                WHERE member.role IN ('owner', 'admin') AND holder.status = 'active'
            ) AS eligible_administrators,
            count(*) FILTER (
                WHERE member.role IN ('owner', 'admin') AND holder.status = 'active'
                  AND member.user_id = o.owner_id
            ) AS owner_is_eligible
        FROM organizations o
        LEFT JOIN organization_members member ON member.org_id = o.id
        LEFT JOIN users holder ON holder.uuid = member.user_id
        GROUP BY o.id, o.owner_id
    ),
    current_findings AS (
        -- The findings are mutually exclusive by construction: one CASE over
        -- one row per organization can only ever yield one of them. The
        -- previous shape kept three disjoint WHERE clauses in step by hand,
        -- which is the kind of agreement that silently stops holding. An
        -- organization is therefore never counted twice, so the row count this
        -- function returns is an organization count -- the number an operator
        -- sizes the repair from.
        SELECT
            org_id,
            CASE
                -- No administrative membership exists at all. Repairing this
                -- means choosing a user and granting them authority.
                WHEN administrative_members = 0
                    THEN 'organization_without_administrator'
                -- Administrative memberships exist, but every holder is
                -- deleted, suspended or otherwise not active. Materially
                -- cheaper to repair -- reactivating one identity may be
                -- enough -- and a different operator decision, which is why it
                -- is reported apart from the case above rather than folded in.
                WHEN eligible_administrators = 0
                    THEN 'organization_without_an_eligible_administrator'
                -- Somebody eligible can administer this organization, but it is
                -- not the owner of record: either the owner holds no
                -- administrative membership, or holds one whose identity is not
                -- active. owner_status and membership_role tell those apart.
                ELSE 'owner_of_record_is_not_an_administrator'
            END AS finding,
            jsonb_build_object(
                'owner_id', owner_id,
                'membership_role', owner_membership_role,
                'owner_status', owner_status,
                'administrative_members', administrative_members,
                'eligible_administrators', eligible_administrators
            ) AS detail
        FROM org_administration
        WHERE administrative_members  = 0
           OR eligible_administrators = 0
           OR owner_is_eligible       = 0
    ),
    resolved AS (
        DELETE FROM membership_integrity_findings f
        WHERE NOT EXISTS (
            SELECT 1 FROM current_findings c
            WHERE c.org_id = f.org_id AND c.finding = f.finding
        )
    )
    INSERT INTO membership_integrity_findings (org_id, finding, detail)
    SELECT org_id, finding, detail FROM current_findings
    ON CONFLICT (org_id, finding) DO UPDATE SET detail = EXCLUDED.detail;

    SELECT count(*) INTO outstanding FROM membership_integrity_findings;
    RETURN outstanding;
END
$function$;
