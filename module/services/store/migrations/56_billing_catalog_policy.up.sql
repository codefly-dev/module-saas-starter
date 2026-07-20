-- Server-owned billing catalog policy.
--
-- Browsers select only a plan key. Price, trial duration, currency, tax
-- behavior, and checkout availability are controlled by this catalog row.

ALTER TABLE plans
    ADD COLUMN checkout_enabled BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN trial_days INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN tax_behavior TEXT NOT NULL DEFAULT 'unspecified',
    ADD CONSTRAINT plans_trial_days_check CHECK (trial_days BETWEEN 0 AND 730),
    ADD CONSTRAINT plans_tax_behavior_check
        CHECK (tax_behavior IN ('unspecified', 'inclusive', 'exclusive', 'automatic'));

UPDATE plans SET checkout_enabled = FALSE WHERE name = 'free';
UPDATE plans SET trial_days = 14 WHERE name = 'pro';
