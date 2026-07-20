DROP POLICY IF EXISTS role_permissions_insert ON role_permissions;
DROP POLICY IF EXISTS role_permissions_select ON role_permissions;
ALTER TABLE role_permissions NO FORCE ROW LEVEL SECURITY;
ALTER TABLE role_permissions DISABLE ROW LEVEL SECURITY;
