-- Converge databases that previously applied the specialized webhook queue
-- onto the generic job platform. Fresh databases already receive this final
-- shape from migrations 15 and 59, so every operation is idempotent.

DROP INDEX IF EXISTS idx_webhook_deliveries_retry;
DROP INDEX IF EXISTS idx_webhook_deliveries_ready;
DROP INDEX IF EXISTS idx_webhook_deliveries_live_lease;

-- Legacy pending/in-flight rows do not carry a generic job identity or fencing
-- token. Preserve their immutable history as failed rather than pretending
-- they were delivered; an administrator can create an explicit replay.
UPDATE webhook_deliveries
SET status = 'failed'
WHERE status <> 'delivered';

ALTER TABLE webhook_deliveries
    DROP CONSTRAINT IF EXISTS webhook_deliveries_status_check;

ALTER TABLE webhook_deliveries
    ADD CONSTRAINT webhook_deliveries_status_check CHECK (
        status IN ('pending', 'delivered', 'failed')
    );

ALTER TABLE webhook_deliveries
    DROP COLUMN IF EXISTS dead_lettered_at,
    DROP COLUMN IF EXISTS last_error,
    DROP COLUMN IF EXISTS lease_expires_at,
    DROP COLUMN IF EXISTS lease_owner,
    DROP COLUMN IF EXISTS next_retry_at,
    DROP COLUMN IF EXISTS max_attempts;

COMMENT ON TABLE webhook_deliveries IS
    'Customer-visible webhook history; generic job tables exclusively own execution lifecycle';

COMMENT ON COLUMN webhook_deliveries.attempts IS
    'Latest generic-job attempt projected for customer-visible history';

REVOKE ALL PRIVILEGES ON webhook_subscriptions, webhook_deliveries
    FROM app_webhook_worker;
GRANT SELECT ON webhook_subscriptions TO app_webhook_worker;
GRANT SELECT ON webhook_deliveries TO app_webhook_worker;
GRANT UPDATE (
    status, http_status, response_body, attempts,
    last_attempt_at, delivered_at, updated_at
) ON webhook_deliveries TO app_webhook_worker;
