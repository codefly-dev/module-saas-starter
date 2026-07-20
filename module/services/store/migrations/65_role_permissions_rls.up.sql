-- role_permissions inherits its authority from the parent role. Reads may see
-- built-in permissions and the current tenant's custom-role permissions;
-- request writes may target only a custom role owned by the current tenant.

ALTER TABLE role_permissions ENABLE ROW LEVEL SECURITY;
ALTER TABLE role_permissions FORCE ROW LEVEL SECURITY;

CREATE POLICY role_permissions_select ON role_permissions
    FOR SELECT
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

CREATE POLICY role_permissions_insert ON role_permissions
    FOR INSERT
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
