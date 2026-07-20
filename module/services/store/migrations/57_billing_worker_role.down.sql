DO $$
DECLARE
    cur TEXT := current_user;
BEGIN
    EXECUTE format('REVOKE app_billing_worker FROM %I', cur);
END $$;

REVOKE ALL PRIVILEGES ON plans, organizations, users, subscriptions
    FROM app_billing_worker;
REVOKE USAGE ON SCHEMA public FROM app_billing_worker;
DROP ROLE IF EXISTS app_billing_worker;
