DROP POLICY IF EXISTS onboarding_progress_user ON onboarding_progress;
ALTER TABLE onboarding_progress NO FORCE ROW LEVEL SECURITY;
ALTER TABLE onboarding_progress DISABLE ROW LEVEL SECURITY;
