REVOKE ALL PRIVILEGES ON webhook_subscriptions, webhook_deliveries
    FROM app_webhook_worker;
GRANT SELECT ON webhook_subscriptions TO app_webhook_worker;
GRANT SELECT, UPDATE ON webhook_deliveries TO app_webhook_worker;

COMMENT ON TABLE webhook_deliveries IS NULL;
COMMENT ON COLUMN webhook_deliveries.attempts IS NULL;

ALTER TABLE webhook_deliveries
    ADD COLUMN IF NOT EXISTS max_attempts INT NOT NULL DEFAULT 5,
    ADD COLUMN IF NOT EXISTS next_retry_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS lease_owner TEXT,
    ADD COLUMN IF NOT EXISTS lease_expires_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS last_error TEXT,
    ADD COLUMN IF NOT EXISTS dead_lettered_at TIMESTAMPTZ;

ALTER TABLE webhook_deliveries
    DROP CONSTRAINT IF EXISTS webhook_deliveries_status_check;

ALTER TABLE webhook_deliveries
    ADD CONSTRAINT webhook_deliveries_status_check CHECK (
        status IN (
            'pending', 'processing', 'retrying', 'delivered',
            'failed', 'dead_letter'
        )
    );

CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_ready
    ON webhook_deliveries(next_retry_at, created_at)
    WHERE status IN ('pending', 'retrying');

CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_live_lease
    ON webhook_deliveries(subscription_id, lease_expires_at)
    WHERE status = 'processing';
