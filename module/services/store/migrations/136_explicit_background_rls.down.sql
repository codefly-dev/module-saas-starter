-- A managed install needs these policies; never silently disable its workers.
DO $guard$ BEGIN
 IF EXISTS (SELECT FROM pg_roles WHERE rolname IN
 ('app_control_plane','app_billing_worker','app_webhook_worker','app_job_worker')
 AND NOT rolbypassrls) THEN
  RAISE EXCEPTION 'background policy rollback requires all legacy bypass roles; use a forward fix';
 END IF;
END $guard$;
DROP POLICY app_billing_worker_explicit_rows ON public.organizations;
DROP POLICY app_billing_worker_explicit_rows ON public.subscriptions;
DROP POLICY app_billing_worker_explicit_rows ON public.users;
DROP POLICY app_control_plane_explicit_rows ON public.actor_chain_journal;
DROP POLICY app_control_plane_explicit_rows ON public.actor_chain_revocations;
DROP POLICY app_control_plane_explicit_rows ON public.api_keys;
DROP POLICY app_control_plane_explicit_rows ON public.approval_decisions;
DROP POLICY app_control_plane_explicit_rows ON public.approval_requests;
DROP POLICY app_control_plane_explicit_rows ON public.audit_event_idempotency;
DROP POLICY app_control_plane_explicit_rows ON public.audit_events;
DROP POLICY app_control_plane_explicit_rows ON public.connector_credentials;
DROP POLICY app_control_plane_explicit_rows ON public.dashboards;
DROP POLICY app_control_plane_explicit_rows ON public.datasource_sources;
DROP POLICY app_control_plane_explicit_rows ON public.delegation_grants;
DROP POLICY app_control_plane_explicit_rows ON public.domain_events;
DROP POLICY app_control_plane_explicit_rows ON public.entitlement_overrides;
DROP POLICY app_control_plane_explicit_rows ON public.execution_custody;
DROP POLICY app_control_plane_explicit_rows ON public.gdpr_requests;
DROP POLICY app_control_plane_explicit_rows ON public.installations;
DROP POLICY app_control_plane_explicit_rows ON public.invitations;
DROP POLICY app_control_plane_explicit_rows ON public.magic_links;
DROP POLICY app_control_plane_explicit_rows ON public.membership_integrity_findings;
DROP POLICY app_control_plane_explicit_rows ON public.mfa_backup_codes;
DROP POLICY app_control_plane_explicit_rows ON public.mfa_devices;
DROP POLICY app_control_plane_explicit_rows ON public.mfa_login_transactions;
DROP POLICY app_control_plane_explicit_rows ON public.notifications;
DROP POLICY app_control_plane_explicit_rows ON public.onboarding_progress;
DROP POLICY app_control_plane_explicit_rows ON public.org_generic_settings;
DROP POLICY app_control_plane_explicit_rows ON public.org_identity_providers;
DROP POLICY app_control_plane_explicit_rows ON public.org_settings;
DROP POLICY app_control_plane_explicit_rows ON public.organization_activations;
DROP POLICY app_control_plane_explicit_rows ON public.organization_authorization_revisions;
DROP POLICY app_control_plane_explicit_rows ON public.organization_members;
DROP POLICY app_control_plane_explicit_rows ON public.organizations;
DROP POLICY app_control_plane_explicit_rows ON public.principal_authorization_revisions;
DROP POLICY app_control_plane_explicit_rows ON public.principals;
DROP POLICY app_control_plane_explicit_rows ON public.record_shares;
DROP POLICY app_control_plane_explicit_rows ON public.role_assignments;
DROP POLICY app_control_plane_explicit_rows ON public.role_permissions;
DROP POLICY app_control_plane_explicit_rows ON public.roles;
DROP POLICY app_control_plane_explicit_rows ON public.scope_grants;
DROP POLICY app_control_plane_explicit_rows ON public.scope_nodes;
DROP POLICY app_control_plane_explicit_rows ON public.sessions;
DROP POLICY app_control_plane_explicit_rows ON public.subscriptions;
DROP POLICY app_control_plane_explicit_rows ON public.team_members;
DROP POLICY app_control_plane_explicit_rows ON public.team_membership_quarantine;
DROP POLICY app_control_plane_explicit_rows ON public.teams;
DROP POLICY app_control_plane_explicit_rows ON public.usage_events;
DROP POLICY app_control_plane_explicit_rows ON public.usage_totals;
DROP POLICY app_control_plane_explicit_rows ON public.user_consent_events;
DROP POLICY app_control_plane_explicit_rows ON public.user_consent_preferences;
DROP POLICY app_control_plane_explicit_rows ON public.user_identities;
DROP POLICY app_control_plane_explicit_rows ON public.users;
DROP POLICY app_control_plane_explicit_rows ON public.waitlist_entries;
DROP POLICY app_control_plane_explicit_rows ON public.webauthn_ceremonies;
DROP POLICY app_control_plane_explicit_rows ON public.webauthn_credentials;
DROP POLICY app_control_plane_explicit_rows ON public.webhook_deliveries;
DROP POLICY app_control_plane_explicit_rows ON public.webhook_subscriptions;
DROP POLICY app_control_plane_explicit_rows ON public.work_context_replay;
DROP POLICY app_job_worker_explicit_rows ON public.approval_requests;
DROP POLICY app_job_worker_explicit_rows ON public.domain_events;
DROP POLICY app_job_worker_explicit_rows ON public.job_messages;
DROP POLICY app_job_worker_explicit_rows ON public.webhook_deliveries;
DROP POLICY app_job_worker_explicit_rows ON public.webhook_subscriptions;
DROP POLICY app_webhook_worker_explicit_rows ON public.webhook_deliveries;
DROP POLICY app_webhook_worker_explicit_rows ON public.webhook_subscriptions;
ALTER FUNCTION public.publish_domain_event(uuid,text,text,text,timestamp with time zone,text,text,text,bytea,uuid,text,text,text,text,text,text,text,integer,bytea) OWNER TO CURRENT_USER;
ALTER FUNCTION public.enqueue_job_message(text,text,uuid,uuid,text,text,text,text,text,integer,bytea,text,jsonb,smallint,integer,timestamp with time zone,bytea) OWNER TO CURRENT_USER;
ALTER FUNCTION public.replay_job_message(uuid,text,timestamp with time zone,bytea) OWNER TO CURRENT_USER;
DROP POLICY webhook_subscriptions_migration_owner_read ON public.webhook_subscriptions;
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
    IF NOT EXISTS (
        SELECT 1 FROM pg_roles
        WHERE rolname = current_user AND (rolbypassrls OR rolsuper)
    ) THEN
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
