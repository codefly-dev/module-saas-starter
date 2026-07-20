-- Product-visible outbound webhook history. The generic job platform owns
-- queue state, ordering, claims, leases, retries, and dead letters.

ALTER TABLE webhook_deliveries
    ADD COLUMN outbox_event_id UUID,
    ADD COLUMN last_attempt_at TIMESTAMPTZ,
    ADD COLUMN updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP;

-- One history row per (domain event, endpoint). Manual replay keeps the stable
-- event_id but leaves outbox_event_id NULL so it creates a distinct row.
CREATE UNIQUE INDEX uq_webhook_outbox_event_subscription
    ON webhook_deliveries(subscription_id, outbox_event_id)
    WHERE outbox_event_id IS NOT NULL;

-- Least-privilege cross-tenant role for the webhook result projection. It can
-- resolve endpoint configuration and update delivery history, but has no job
-- table authority; app_job_worker owns the generic execution lifecycle.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'app_webhook_worker') THEN
        CREATE ROLE app_webhook_worker NOLOGIN NOINHERIT BYPASSRLS;
    ELSE
        ALTER ROLE app_webhook_worker NOLOGIN NOINHERIT BYPASSRLS;
    END IF;
END $$;

GRANT USAGE ON SCHEMA public TO app_webhook_worker;
GRANT SELECT ON webhook_subscriptions TO app_webhook_worker;
GRANT SELECT ON webhook_deliveries TO app_webhook_worker;
GRANT UPDATE (
    status, http_status, response_body, attempts,
    last_attempt_at, delivered_at, updated_at
) ON webhook_deliveries TO app_webhook_worker;

DO $$
DECLARE
    cur TEXT := current_user;
BEGIN
    EXECUTE format('GRANT app_webhook_worker TO %I', cur);
END $$;
