-- Phase 2A — RLS on webhook subsystem.
--
-- webhook_subscriptions: direct org_id, standard policy.
-- webhook_deliveries: no direct org_id; tenant inferred via the
--   parent subscription. Uses an EXISTS subquery in the policy.

ALTER TABLE webhook_subscriptions ENABLE ROW LEVEL SECURITY;
ALTER TABLE webhook_subscriptions FORCE  ROW LEVEL SECURITY;

CREATE POLICY webhook_subscriptions_tenant ON webhook_subscriptions
    USING (
        org_id::text = current_setting('app.current_org_id', true)
        OR current_setting('app.bypass', true) = '1'
    )
    WITH CHECK (
        org_id::text = current_setting('app.current_org_id', true)
        OR current_setting('app.bypass', true) = '1'
    );

ALTER TABLE webhook_deliveries ENABLE ROW LEVEL SECURITY;
ALTER TABLE webhook_deliveries FORCE  ROW LEVEL SECURITY;

-- The JOIN policy recursively re-applies webhook_subscriptions's
-- policy: a delivery row is visible iff its parent subscription is
-- visible to the current setting. EXISTS is short-circuit, indexed
-- on subscription_id, ~no perf cost.
CREATE POLICY webhook_deliveries_tenant ON webhook_deliveries
    USING (
        current_setting('app.bypass', true) = '1'
        OR EXISTS (
            SELECT 1 FROM webhook_subscriptions ws
            WHERE ws.id = webhook_deliveries.subscription_id
              AND ws.org_id::text = current_setting('app.current_org_id', true)
        )
    )
    WITH CHECK (
        current_setting('app.bypass', true) = '1'
        OR EXISTS (
            SELECT 1 FROM webhook_subscriptions ws
            WHERE ws.id = webhook_deliveries.subscription_id
              AND ws.org_id::text = current_setting('app.current_org_id', true)
        )
    );
