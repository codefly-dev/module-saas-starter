-- Phase 2C — RLS on onboarding_progress (user-scoped).
-- Visible/writable only as the owning user (app.current_user_id), or System bypass.

ALTER TABLE onboarding_progress ENABLE ROW LEVEL SECURITY;
ALTER TABLE onboarding_progress FORCE  ROW LEVEL SECURITY;

CREATE POLICY onboarding_progress_user ON onboarding_progress
    USING (
        user_id::text = current_setting('app.current_user_id', true)
        OR current_setting('app.bypass', true) = '1'
    )
    WITH CHECK (
        user_id::text = current_setting('app.current_user_id', true)
        OR current_setting('app.bypass', true) = '1'
    );
