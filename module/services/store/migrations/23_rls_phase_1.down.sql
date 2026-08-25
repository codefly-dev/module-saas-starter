DROP POLICY IF EXISTS audit_export_configs_tenant ON audit_export_configs;
ALTER TABLE audit_export_configs DISABLE ROW LEVEL SECURITY;
