DO $$
DECLARE
    t TEXT;
BEGIN
    FOREACH t IN ARRAY ARRAY[
        'notifications',
        'mfa_devices',
        'mfa_backup_codes'
    ] LOOP
        EXECUTE format('DROP POLICY IF EXISTS %I_user ON %I', t, t);
        EXECUTE format('ALTER TABLE %I NO FORCE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE %I DISABLE ROW LEVEL SECURITY', t);
    END LOOP;
END $$;
