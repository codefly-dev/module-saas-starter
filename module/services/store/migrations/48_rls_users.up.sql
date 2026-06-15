-- Phase 2C — RLS on users. users has no org_id (a user can belong to many orgs),
-- so the policy is: self, or someone you share an org with (co-member — keeps
-- org-member listings working), or System bypass (pre-auth login/registration,
-- admin). Writes are self-or-System only.

ALTER TABLE users ENABLE ROW LEVEL SECURITY;
ALTER TABLE users FORCE  ROW LEVEL SECURITY;

CREATE POLICY users_access ON users
    USING (
        current_setting('app.bypass', true) = '1'
        OR uuid::text = current_setting('app.current_user_id', true)
        OR EXISTS (
            SELECT 1
            FROM organization_members om_self
            JOIN organization_members om_other ON om_self.org_id = om_other.org_id
            WHERE om_self.user_id::text = current_setting('app.current_user_id', true)
              AND om_other.user_id = users.uuid
        )
    )
    WITH CHECK (
        current_setting('app.bypass', true) = '1'
        OR uuid::text = current_setting('app.current_user_id', true)
    );
