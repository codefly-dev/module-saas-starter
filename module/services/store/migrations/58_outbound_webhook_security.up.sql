-- Encrypt outbound-webhook signing material and retain one previous key for a
-- bounded rotation overlap. Existing plaintext values are upgraded through
-- Vault by accounts startup before the service begins accepting traffic.

ALTER TABLE webhook_subscriptions
    RENAME COLUMN secret TO secret_encrypted;

ALTER TABLE webhook_subscriptions
    ADD COLUMN previous_secret_encrypted TEXT,
    ADD COLUMN previous_secret_expires_at TIMESTAMPTZ;

ALTER TABLE webhook_subscriptions
    ADD CONSTRAINT webhook_previous_secret_pair CHECK (
        (previous_secret_encrypted IS NULL AND previous_secret_expires_at IS NULL)
        OR
        (previous_secret_encrypted IS NOT NULL AND previous_secret_expires_at IS NOT NULL)
    );

COMMENT ON COLUMN webhook_subscriptions.secret_encrypted IS
    'Versioned Vault/KMS envelope; never plaintext after accounts startup';
COMMENT ON COLUMN webhook_subscriptions.previous_secret_encrypted IS
    'Prior Vault/KMS envelope, accepted only until previous_secret_expires_at';

-- An event ID is stable across endpoint fan-out and manual replay. The
-- delivery ID remains unique to one endpoint attempt history.
ALTER TABLE webhook_deliveries
    ADD COLUMN event_id UUID;

UPDATE webhook_deliveries
SET event_id = id
WHERE event_id IS NULL;

ALTER TABLE webhook_deliveries
    ALTER COLUMN event_id SET NOT NULL;

CREATE INDEX idx_webhook_deliveries_event
    ON webhook_deliveries(event_id);
