-- Stripe billing integration.
--
-- Plans get optional stripe_price_id so a Stripe-driven CreateCheckoutSession
-- can look up the price. Organizations get a stripe_customer_id so a single
-- customer record in Stripe tracks all subscriptions + invoices for the org.
--
-- Free plans carry no stripe_price_id — they're internal-only. Pro and above
-- get a real Stripe price id populated by the ops team after creating the
-- price in the Stripe dashboard (or via a seed script).

ALTER TABLE plans
    ADD COLUMN stripe_product_id TEXT,
    ADD COLUMN stripe_price_id   TEXT;

CREATE INDEX idx_plans_stripe_price ON plans(stripe_price_id) WHERE stripe_price_id IS NOT NULL;

ALTER TABLE organizations
    ADD COLUMN stripe_customer_id TEXT;

CREATE UNIQUE INDEX idx_organizations_stripe_customer
    ON organizations(stripe_customer_id) WHERE stripe_customer_id IS NOT NULL;
