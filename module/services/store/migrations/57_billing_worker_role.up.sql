-- Least-privilege cross-tenant role for Stripe subscription projection. It
-- bypasses RLS deliberately, but can touch only the product tables required
-- to resolve and reconcile current subscription state. Generic job receipt,
-- claims, and lifecycle belong exclusively to app_job_worker.

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'app_billing_worker') THEN
        CREATE ROLE app_billing_worker NOLOGIN NOINHERIT BYPASSRLS;
    ELSE
        ALTER ROLE app_billing_worker NOLOGIN NOINHERIT BYPASSRLS;
    END IF;
END $$;

GRANT USAGE ON SCHEMA public TO app_billing_worker;
GRANT SELECT ON plans, organizations, users TO app_billing_worker;
GRANT SELECT, INSERT, UPDATE ON subscriptions TO app_billing_worker;

-- The Codefly-managed database principal owns the physical connection and
-- assumes this role on checkout from the dedicated worker pool.
DO $$
DECLARE
    cur TEXT := current_user;
BEGIN
    EXECUTE format('GRANT app_billing_worker TO %I', cur);
END $$;
