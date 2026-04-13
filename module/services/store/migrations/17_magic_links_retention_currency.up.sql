-- Migration 17: Magic links, data retention policies, multi-currency billing

-- Magic links table for passwordless authentication.
CREATE TABLE IF NOT EXISTS magic_links (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email       TEXT NOT NULL,
    token_hash  TEXT NOT NULL UNIQUE,
    expires_at  TIMESTAMPTZ NOT NULL,
    used_at     TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_magic_links_token_hash ON magic_links(token_hash);
CREATE INDEX idx_magic_links_email ON magic_links(email);

-- Data retention policies table.
CREATE TABLE IF NOT EXISTS data_retention_policies (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    resource_type   TEXT NOT NULL UNIQUE,
    retention_days  INT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Seed default retention policies.
INSERT INTO data_retention_policies (resource_type, retention_days) VALUES
    ('audit_events', 365),
    ('sessions', 90),
    ('webhook_deliveries', 30),
    ('notifications', 90)
ON CONFLICT (resource_type) DO NOTHING;

-- Multi-currency: add currency column to plans table.
ALTER TABLE plans ADD COLUMN IF NOT EXISTS currency TEXT NOT NULL DEFAULT 'usd';
