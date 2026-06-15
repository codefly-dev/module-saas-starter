DROP POLICY IF EXISTS delegation_grants_tenant ON delegation_grants;
ALTER TABLE delegation_grants NO FORCE ROW LEVEL SECURITY;
ALTER TABLE delegation_grants DISABLE ROW LEVEL SECURITY;
