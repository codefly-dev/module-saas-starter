ALTER TABLE plans
    DROP CONSTRAINT IF EXISTS plans_tax_behavior_check,
    DROP CONSTRAINT IF EXISTS plans_trial_days_check,
    DROP COLUMN IF EXISTS tax_behavior,
    DROP COLUMN IF EXISTS trial_days,
    DROP COLUMN IF EXISTS checkout_enabled;
