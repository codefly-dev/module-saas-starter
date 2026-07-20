DROP INDEX IF EXISTS idx_webhook_deliveries_event;

ALTER TABLE webhook_deliveries
    DROP COLUMN IF EXISTS event_id;

ALTER TABLE webhook_subscriptions
    DROP CONSTRAINT IF EXISTS webhook_previous_secret_pair,
    DROP COLUMN IF EXISTS previous_secret_expires_at,
    DROP COLUMN IF EXISTS previous_secret_encrypted;

ALTER TABLE webhook_subscriptions
    RENAME COLUMN secret_encrypted TO secret;
