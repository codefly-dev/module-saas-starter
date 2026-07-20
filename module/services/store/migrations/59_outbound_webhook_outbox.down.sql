DO $$
DECLARE
    cur TEXT := current_user;
BEGIN
    EXECUTE format('REVOKE app_webhook_worker FROM %I', cur);
END $$;

REVOKE ALL PRIVILEGES ON webhook_subscriptions, webhook_deliveries
    FROM app_webhook_worker;
REVOKE USAGE ON SCHEMA public FROM app_webhook_worker;
DROP ROLE IF EXISTS app_webhook_worker;

DROP INDEX IF EXISTS uq_webhook_outbox_event_subscription;

ALTER TABLE webhook_deliveries
    DROP COLUMN IF EXISTS updated_at,
    DROP COLUMN IF EXISTS last_attempt_at,
    DROP COLUMN IF EXISTS outbox_event_id;
