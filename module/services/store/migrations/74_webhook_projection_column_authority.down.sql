REVOKE UPDATE (
    status, http_status, response_body, attempts,
    last_attempt_at, delivered_at, updated_at
) ON webhook_deliveries FROM app_webhook_worker;

GRANT UPDATE ON webhook_deliveries TO app_webhook_worker;
