DROP POLICY IF EXISTS webhook_deliveries_tenant ON webhook_deliveries;
ALTER TABLE webhook_deliveries DISABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS webhook_subscriptions_tenant ON webhook_subscriptions;
ALTER TABLE webhook_subscriptions DISABLE ROW LEVEL SECURITY;
