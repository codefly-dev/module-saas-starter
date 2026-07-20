ALTER POLICY audit_export_configs_tenant ON audit_export_configs
    USING (
        org_id::text = current_setting('app.current_org_id', true)
        OR current_setting('app.bypass', true) = '1'
    )
    WITH CHECK (
        org_id::text = current_setting('app.current_org_id', true)
        OR current_setting('app.bypass', true) = '1'
    );

ALTER POLICY webhook_subscriptions_tenant ON webhook_subscriptions
    USING (
        org_id::text = current_setting('app.current_org_id', true)
        OR current_setting('app.bypass', true) = '1'
    )
    WITH CHECK (
        org_id::text = current_setting('app.current_org_id', true)
        OR current_setting('app.bypass', true) = '1'
    );

ALTER POLICY webhook_deliveries_tenant ON webhook_deliveries
    USING (
        current_setting('app.bypass', true) = '1'
        OR EXISTS (
            SELECT 1 FROM webhook_subscriptions ws
            WHERE ws.id = webhook_deliveries.subscription_id
              AND ws.org_id::text = current_setting('app.current_org_id', true)
        )
    )
    WITH CHECK (
        current_setting('app.bypass', true) = '1'
        OR EXISTS (
            SELECT 1 FROM webhook_subscriptions ws
            WHERE ws.id = webhook_deliveries.subscription_id
              AND ws.org_id::text = current_setting('app.current_org_id', true)
        )
    );

ALTER POLICY api_keys_tenant ON api_keys
    USING (
        organization_id::text = current_setting('app.current_org_id', true)
        OR current_setting('app.bypass', true) = '1'
    )
    WITH CHECK (
        organization_id::text = current_setting('app.current_org_id', true)
        OR current_setting('app.bypass', true) = '1'
    );

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
                USING (
                    org_id::text = current_setting('app.current_org_id', true)
                    OR current_setting('app.bypass', true) = '1'
                )
                WITH CHECK (
                    org_id::text = current_setting('app.current_org_id', true)
                    OR current_setting('app.bypass', true) = '1'
                )
        $policy$, t, t);
    END LOOP;
END $$;

ALTER POLICY teams_tenant ON teams
    USING (
        org_id::text = current_setting('app.current_org_id', true)
        OR current_setting('app.bypass', true) = '1'
    )
    WITH CHECK (
        org_id::text = current_setting('app.current_org_id', true)
        OR current_setting('app.bypass', true) = '1'
    );

ALTER POLICY team_members_tenant ON team_members
    USING (
        current_setting('app.bypass', true) = '1'
        OR EXISTS (
            SELECT 1 FROM teams t
            WHERE t.id = team_members.team_id
              AND t.org_id::text = current_setting('app.current_org_id', true)
        )
    )
    WITH CHECK (
        current_setting('app.bypass', true) = '1'
        OR EXISTS (
            SELECT 1 FROM teams t
            WHERE t.id = team_members.team_id
              AND t.org_id::text = current_setting('app.current_org_id', true)
        )
    );

ALTER POLICY audit_events_tenant ON audit_events
    USING (
        current_setting('app.bypass', true) = '1'
        OR (
            org_id IS NOT NULL
            AND org_id::text = current_setting('app.current_org_id', true)
        )
    )
    WITH CHECK (
        current_setting('app.bypass', true) = '1'
        OR (
            org_id IS NOT NULL
            AND org_id::text = current_setting('app.current_org_id', true)
        )
    );

ALTER POLICY roles_polymorphic ON roles
    USING (
        current_setting('app.bypass', true) = '1'
        OR org_id IS NULL
        OR org_id::text = current_setting('app.current_org_id', true)
    )
    WITH CHECK (
        current_setting('app.bypass', true) = '1'
        OR (
            org_id IS NOT NULL
            AND org_id::text = current_setting('app.current_org_id', true)
        )
    );

ALTER POLICY role_assignments_polymorphic ON role_assignments
    USING (
        current_setting('app.bypass', true) = '1'
        OR org_id IS NULL
        OR org_id::text = current_setting('app.current_org_id', true)
    )
    WITH CHECK (
        current_setting('app.bypass', true) = '1'
        OR (
            org_id IS NOT NULL
            AND org_id::text = current_setting('app.current_org_id', true)
        )
    );

ALTER POLICY organizations_self ON organizations
    USING (
        current_setting('app.bypass', true) = '1'
        OR id::text = current_setting('app.current_org_id', true)
    )
    WITH CHECK (
        current_setting('app.bypass', true) = '1'
        OR id::text = current_setting('app.current_org_id', true)
    );

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
                USING (
                    current_setting('app.bypass', true) = '1'
                    OR user_id::text = current_setting('app.current_user_id', true)
                )
                WITH CHECK (
                    current_setting('app.bypass', true) = '1'
                    OR user_id::text = current_setting('app.current_user_id', true)
                )
        $policy$, t, t);
    END LOOP;
END $$;

ALTER POLICY principals_access ON principals
    USING (
        current_setting('app.bypass', true) = '1'
        OR org_id::text = current_setting('app.current_org_id', true)
        OR id::text = current_setting('app.current_user_id', true)
        OR EXISTS (
            SELECT 1 FROM organization_members om
            WHERE om.user_id = principals.id
              AND om.org_id::text = current_setting('app.current_org_id', true)
        )
    )
    WITH CHECK (
        current_setting('app.bypass', true) = '1'
        OR org_id::text = current_setting('app.current_org_id', true)
        OR id::text = current_setting('app.current_user_id', true)
    );

ALTER POLICY delegation_grants_tenant ON delegation_grants
    USING (
        org_id::text = current_setting('app.current_org_id', true)
        OR current_setting('app.bypass', true) = '1'
    )
    WITH CHECK (
        org_id::text = current_setting('app.current_org_id', true)
        OR current_setting('app.bypass', true) = '1'
    );

ALTER POLICY magic_links_system ON magic_links
    USING (current_setting('app.bypass', true) = '1')
    WITH CHECK (current_setting('app.bypass', true) = '1');

ALTER POLICY user_identities_user ON user_identities
    USING (
        user_uuid::text = current_setting('app.current_user_id', true)
        OR current_setting('app.bypass', true) = '1'
    )
    WITH CHECK (
        user_uuid::text = current_setting('app.current_user_id', true)
        OR current_setting('app.bypass', true) = '1'
    );

ALTER POLICY users_insert ON users
    WITH CHECK (
        current_setting('app.bypass', true) = '1'
        OR uuid::text = current_setting('app.current_user_id', true)
    );

ALTER POLICY users_update ON users
    USING (
        current_setting('app.bypass', true) = '1'
        OR uuid::text = current_setting('app.current_user_id', true)
    )
    WITH CHECK (
        current_setting('app.bypass', true) = '1'
        OR uuid::text = current_setting('app.current_user_id', true)
    );

ALTER POLICY users_delete ON users
    USING (
        current_setting('app.bypass', true) = '1'
        OR uuid::text = current_setting('app.current_user_id', true)
    );

ALTER POLICY role_permissions_select ON role_permissions
    USING (
        current_setting('app.bypass', true) = '1'
        OR EXISTS (
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
        current_setting('app.bypass', true) = '1'
        OR EXISTS (
            SELECT 1
            FROM roles
            WHERE roles.id = role_permissions.role_id
              AND roles.org_id IS NOT NULL
              AND roles.org_id::text = current_setting('app.current_org_id', true)
        )
    );
