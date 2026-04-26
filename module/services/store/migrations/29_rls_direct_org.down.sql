DO $$
DECLARE
    t TEXT;
BEGIN
    FOREACH t IN ARRAY ARRAY[
        'org_settings',
        'invitations',
        'organization_members',
        'subscriptions',
        'entitlement_overrides',
        'usage_records'
    ] LOOP
        EXECUTE format('DROP POLICY IF EXISTS %I_tenant ON %I', t, t);
        EXECUTE format('ALTER TABLE %I DISABLE ROW LEVEL SECURITY', t);
    END LOOP;
END $$;
