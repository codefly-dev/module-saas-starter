-- Migration 18: Drop the hardcoded onboarding_progress.step_name CHECK.
--
-- Migration 15 pinned step_name to four literal values (create_org,
-- invite_team, choose_plan, setup_api_key). That was useful as early
-- documentation but it also made the feature impossible to extend and
-- broke every integration test that used any other step name.
--
-- Onboarding is a product concern, not a schema concern — apps building
-- on saas-starter pick their own step taxonomy. Enforcement belongs in
-- the business layer, not a DB CHECK.

ALTER TABLE onboarding_progress DROP CONSTRAINT IF EXISTS onboarding_progress_step_name_check;
