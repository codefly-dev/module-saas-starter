DROP POLICY IF EXISTS users_select ON users;
DROP POLICY IF EXISTS users_insert ON users;
DROP POLICY IF EXISTS users_update ON users;
DROP POLICY IF EXISTS users_delete ON users;
CREATE POLICY users_access ON users
    USING (
        current_setting('app.bypass', true) = '1'
        OR uuid::text = current_setting('app.current_user_id', true)
        OR EXISTS (
            SELECT 1 FROM organization_members om_self
            JOIN organization_members om_other ON om_self.org_id = om_other.org_id
            WHERE om_self.user_id::text = current_setting('app.current_user_id', true)
              AND om_other.user_id = users.uuid
        )
    )
    WITH CHECK (
        current_setting('app.bypass', true) = '1'
        OR uuid::text = current_setting('app.current_user_id', true)
    );
