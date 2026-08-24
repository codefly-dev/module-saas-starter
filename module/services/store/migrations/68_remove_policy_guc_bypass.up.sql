-- RLS policies now express only request-scoped visibility. Cross-tenant work
-- assumes app_control_plane, whose BYPASSRLS attribute is an explicit database
-- capability. A caller can no longer manufacture authority with set_config().

ALTER POLICY webhook_subscriptions_tenant ON webhook_subscriptions
    USING (org_id::text = current_setting('app.current_org_id', true))
    WITH CHECK (org_id::text = current_setting('app.current_org_id', true));

ALTER POLICY webhook_deliveries_tenant ON webhook_deliveries
    USING (
        EXISTS (
            SELECT 1 FROM webhook_subscriptions ws
            WHERE ws.id = webhook_deliveries.subscription_id
              AND ws.org_id::text = current_setting('app.current_org_id', true)
        )
    )
    WITH CHECK (
        EXISTS (
            SELECT 1 FROM webhook_subscriptions ws
            WHERE ws.id = webhook_deliveries.subscription_id
              AND ws.org_id::text = current_setting('app.current_org_id', true)
        )
    );

ALTER POLICY api_keys_tenant ON api_keys
    USING (organization_id::text = current_setting('app.current_org_id', true))
    WITH CHECK (organization_id::text = current_setting('app.current_org_id', true));

DO $$
DECLARE
    t TEXT;
BEGIN
    FOREACH t IN ARRAY ARRAY[
        'org_settings',
        'invitations',
        'organization_members',
        'subscriptions',
        'entitlement_overrides'
    ] LOOP
        EXECUTE format($policy$
            ALTER POLICY %I_tenant ON %I
                USING (org_id::text = current_setting('app.current_org_id', true))
                WITH CHECK (org_id::text = current_setting('app.current_org_id', true))
        $policy$, t, t);
    END LOOP;
END $$;

ALTER POLICY teams_tenant ON teams
    USING (org_id::text = current_setting('app.current_org_id', true))
    WITH CHECK (org_id::text = current_setting('app.current_org_id', true));

ALTER POLICY team_members_tenant ON team_members
    USING (
        EXISTS (
            SELECT 1 FROM teams t
            WHERE t.id = team_members.team_id
              AND t.org_id::text = current_setting('app.current_org_id', true)
        )
    )
    WITH CHECK (
        EXISTS (
            SELECT 1 FROM teams t
            WHERE t.id = team_members.team_id
              AND t.org_id::text = current_setting('app.current_org_id', true)
        )
    );

ALTER POLICY audit_events_tenant ON audit_events
    USING (
        org_id IS NOT NULL
        AND org_id::text = current_setting('app.current_org_id', true)
    )
    WITH CHECK (
        org_id IS NOT NULL
        AND org_id::text = current_setting('app.current_org_id', true)
    );

ALTER POLICY roles_polymorphic ON roles
    USING (
        org_id IS NULL
        OR org_id::text = current_setting('app.current_org_id', true)
    )
    WITH CHECK (
        org_id IS NOT NULL
        AND org_id::text = current_setting('app.current_org_id', true)
    );

ALTER POLICY role_assignments_polymorphic ON role_assignments
    USING (
        org_id IS NULL
        OR org_id::text = current_setting('app.current_org_id', true)
    )
    WITH CHECK (
        org_id IS NOT NULL
        AND org_id::text = current_setting('app.current_org_id', true)
    );

ALTER POLICY organizations_self ON organizations
    USING (id::text = current_setting('app.current_org_id', true))
    WITH CHECK (id::text = current_setting('app.current_org_id', true));

DO $$
DECLARE
    t TEXT;
BEGIN
    FOREACH t IN ARRAY ARRAY[
        'notifications',
        'mfa_devices',
        'mfa_backup_codes',
        'sessions',
        'onboarding_progress',
        'gdpr_requests',
        'mfa_login_transactions',
        'webauthn_credentials',
        'webauthn_ceremonies'
    ] LOOP
        EXECUTE format($policy$
            ALTER POLICY %I_user ON %I
                USING (user_id::text = current_setting('app.current_user_id', true))
                WITH CHECK (user_id::text = current_setting('app.current_user_id', true))
        $policy$, t, t);
    END LOOP;
END $$;

ALTER POLICY principals_access ON principals
    USING (
        org_id::text = current_setting('app.current_org_id', true)
        OR id::text = current_setting('app.current_user_id', true)
        OR EXISTS (
            SELECT 1 FROM organization_members om
            WHERE om.user_id = principals.id
              AND om.org_id::text = current_setting('app.current_org_id', true)
        )
    )
    WITH CHECK (
        org_id::text = current_setting('app.current_org_id', true)
        OR id::text = current_setting('app.current_user_id', true)
    );

ALTER POLICY delegation_grants_tenant ON delegation_grants
    USING (org_id::text = current_setting('app.current_org_id', true))
    WITH CHECK (org_id::text = current_setting('app.current_org_id', true));

-- magic_links is exclusively pre-authentication. app_tenant has no table
-- privileges and the only permitted reader/writer bypasses RLS by role.
ALTER POLICY magic_links_system ON magic_links
    USING (false)
    WITH CHECK (false);

ALTER POLICY user_identities_user ON user_identities
    USING (user_uuid::text = current_setting('app.current_user_id', true))
    WITH CHECK (user_uuid::text = current_setting('app.current_user_id', true));

ALTER POLICY users_insert ON users
    WITH CHECK (uuid::text = current_setting('app.current_user_id', true));

ALTER POLICY users_update ON users
    USING (uuid::text = current_setting('app.current_user_id', true))
    WITH CHECK (uuid::text = current_setting('app.current_user_id', true));

ALTER POLICY users_delete ON users
    USING (uuid::text = current_setting('app.current_user_id', true));

ALTER POLICY role_permissions_select ON role_permissions
    USING (
        EXISTS (
            SELECT 1
            FROM roles
            WHERE roles.id = role_permissions.role_id
              AND (
                  roles.org_id IS NULL
                  OR roles.org_id::text = current_setting('app.current_org_id', true)
              )
        )
    );

ALTER POLICY role_permissions_insert ON role_permissions
    WITH CHECK (
        EXISTS (
            SELECT 1
            FROM roles
            WHERE roles.id = role_permissions.role_id
              AND roles.org_id IS NOT NULL
              AND roles.org_id::text = current_setting('app.current_org_id', true)
        )
    );
