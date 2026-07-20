DROP INDEX IF EXISTS idx_subscriptions_org_active;
CREATE UNIQUE INDEX idx_subscriptions_org_active
    ON subscriptions(org_id)
    WHERE status IN ('active', 'trialing', 'past_due');

DROP INDEX IF EXISTS idx_subscriptions_stripe_id;

ALTER TABLE subscriptions DROP CONSTRAINT subscriptions_status_check;
UPDATE subscriptions
SET status = 'canceled'
WHERE status IN ('incomplete', 'incomplete_expired', 'unpaid', 'paused');
ALTER TABLE subscriptions
    ADD CONSTRAINT subscriptions_status_check
        CHECK (status IN ('active', 'past_due', 'canceled', 'trialing')),
    DROP COLUMN IF EXISTS stripe_state_observed_at;
