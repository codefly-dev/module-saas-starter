-- Phase 2C — RLS on gdpr_requests (user-scoped).
-- Visible/writable only as the subject user (app.current_user_id), or System
-- bypass (background export/deletion processing + point lookups by id).

ALTER TABLE gdpr_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE gdpr_requests FORCE  ROW LEVEL SECURITY;

CREATE POLICY gdpr_requests_user ON gdpr_requests
    USING (
        user_id::text = current_setting('app.current_user_id', true)
        OR current_setting('app.bypass', true) = '1'
    )
    WITH CHECK (
        user_id::text = current_setting('app.current_user_id', true)
        OR current_setting('app.bypass', true) = '1'
    );
