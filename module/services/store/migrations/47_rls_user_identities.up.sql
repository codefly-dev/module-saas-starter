-- Phase 2C — RLS on user_identities (user-scoped via user_uuid).
-- Self or System bypass (pre-auth login/registration resolve identities before
-- a user context exists → those paths use the audited System bypass).

ALTER TABLE user_identities ENABLE ROW LEVEL SECURITY;
ALTER TABLE user_identities FORCE  ROW LEVEL SECURITY;

CREATE POLICY user_identities_user ON user_identities
    USING (
        user_uuid::text = current_setting('app.current_user_id', true)
        OR current_setting('app.bypass', true) = '1'
    )
    WITH CHECK (
        user_uuid::text = current_setting('app.current_user_id', true)
        OR current_setting('app.bypass', true) = '1'
    );
