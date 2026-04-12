-- Plans define product tiers
CREATE TABLE IF NOT EXISTS "plans" (
    id           UUID DEFAULT gen_random_uuid() PRIMARY KEY,
    name         TEXT NOT NULL UNIQUE,
    display_name TEXT NOT NULL,
    is_default   BOOLEAN DEFAULT false,
    sort_order   INTEGER DEFAULT 0,
    created_at   TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

-- What each plan includes
CREATE TABLE IF NOT EXISTS "plan_entitlements" (
    plan_id     UUID NOT NULL REFERENCES plans(id) ON DELETE CASCADE,
    feature     TEXT NOT NULL,
    limit_value BIGINT,
    PRIMARY KEY (plan_id, feature)
);

-- Organization subscriptions (one active per org)
CREATE TABLE IF NOT EXISTS "subscriptions" (
    id                    UUID DEFAULT gen_random_uuid() PRIMARY KEY,
    org_id                UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    plan_id               UUID NOT NULL REFERENCES plans(id),
    status                TEXT NOT NULL DEFAULT 'active'
                          CHECK (status IN ('active', 'past_due', 'canceled', 'trialing')),
    stripe_subscription_id TEXT,
    current_period_start  TIMESTAMP WITH TIME ZONE,
    current_period_end    TIMESTAMP WITH TIME ZONE,
    created_at            TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    updated_at            TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX idx_subscriptions_org_active
    ON subscriptions(org_id) WHERE status IN ('active', 'trialing', 'past_due');

-- Per-org entitlement overrides (enterprise deals, trials)
CREATE TABLE IF NOT EXISTS "entitlement_overrides" (
    id          UUID DEFAULT gen_random_uuid() PRIMARY KEY,
    org_id      UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    feature     TEXT NOT NULL,
    limit_value BIGINT,
    reason      TEXT,
    created_by  UUID REFERENCES users(uuid),
    created_at  TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    expires_at  TIMESTAMP WITH TIME ZONE,
    UNIQUE(org_id, feature)
);

-- Usage tracking for metered features
CREATE TABLE IF NOT EXISTS "usage_records" (
    org_id      UUID NOT NULL,
    feature     TEXT NOT NULL,
    period      TEXT NOT NULL,
    quantity    BIGINT NOT NULL DEFAULT 0,
    updated_at  TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (org_id, feature, period)
);

CREATE INDEX idx_usage_records_org_period ON usage_records(org_id, period);

-- Seed plans
INSERT INTO plans (name, display_name, is_default, sort_order) VALUES
    ('free',       'Free',       true,  0),
    ('pro',        'Pro',        false, 1),
    ('enterprise', 'Enterprise', false, 2)
ON CONFLICT (name) DO NOTHING;

-- Seed entitlements (NULL = unlimited)
INSERT INTO plan_entitlements (plan_id, feature, limit_value)
SELECT p.id, e.feature, e.limit_value
FROM plans p
JOIN (VALUES
    ('free',       'seats',              5),
    ('free',       'api_keys',           2),
    ('free',       'api_calls_monthly',  10000),
    ('free',       'sso',                0),
    ('free',       'audit_log',          0),
    ('pro',        'seats',              50),
    ('pro',        'api_keys',           20),
    ('pro',        'api_calls_monthly',  500000),
    ('pro',        'sso',                1),
    ('pro',        'audit_log',          1),
    ('enterprise', 'seats',              NULL),
    ('enterprise', 'api_keys',           NULL),
    ('enterprise', 'api_calls_monthly',  NULL),
    ('enterprise', 'sso',                1),
    ('enterprise', 'audit_log',          1)
) AS e(plan_name, feature, limit_value) ON p.name = e.plan_name
ON CONFLICT DO NOTHING;
