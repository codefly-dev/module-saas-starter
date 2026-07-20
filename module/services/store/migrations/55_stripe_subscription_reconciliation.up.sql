-- Monotonic Stripe subscription projection.
--
-- Stripe does not guarantee webhook delivery order. Retain when a worker began
-- observing current provider state and reject older writes that finish later.
-- Stripe subscription ids are globally unique and become the durable identity
-- of each local subscription history row.

ALTER TABLE subscriptions
    DROP CONSTRAINT subscriptions_status_check,
    ADD COLUMN stripe_state_observed_at TIMESTAMPTZ;

ALTER TABLE subscriptions
    ADD CONSTRAINT subscriptions_status_check CHECK (status IN (
        'incomplete', 'incomplete_expired', 'trialing', 'active',
        'past_due', 'canceled', 'unpaid', 'paused'
    ));

UPDATE subscriptions
SET stripe_state_observed_at = COALESCE(updated_at, created_at)
WHERE stripe_subscription_id IS NOT NULL;

CREATE UNIQUE INDEX idx_subscriptions_stripe_id
    ON subscriptions(stripe_subscription_id)
    WHERE stripe_subscription_id IS NOT NULL;

DROP INDEX idx_subscriptions_org_active;
CREATE UNIQUE INDEX idx_subscriptions_org_active
    ON subscriptions(org_id)
    WHERE status IN ('incomplete', 'trialing', 'active', 'past_due', 'unpaid', 'paused');
