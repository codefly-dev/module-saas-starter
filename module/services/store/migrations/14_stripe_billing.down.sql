DROP TABLE IF EXISTS stripe_webhook_events;

DROP INDEX IF EXISTS idx_organizations_stripe_customer;
ALTER TABLE organizations DROP COLUMN IF EXISTS stripe_customer_id;

DROP INDEX IF EXISTS idx_plans_stripe_price;
ALTER TABLE plans
    DROP COLUMN IF EXISTS stripe_price_id,
    DROP COLUMN IF EXISTS stripe_product_id;
