-- Restore the hardcoded step_name CHECK that migration 18 dropped.
ALTER TABLE onboarding_progress
    ADD CONSTRAINT onboarding_progress_step_name_check
    CHECK (step_name IN ('create_org', 'invite_team', 'choose_plan', 'setup_api_key'));
