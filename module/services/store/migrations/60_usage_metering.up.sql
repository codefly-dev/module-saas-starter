-- Generic SaaS usage metering.
--
-- usage_events is an immutable idempotency ledger of accepted and rejected
-- consumption attempts. usage_totals is the transactionally maintained read
-- model used for quota checks and dashboards.

CREATE TABLE usage_totals (
    org_id       UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    meter        TEXT NOT NULL CHECK (meter ~ '^[a-z][a-z0-9_]{0,127}$'),
    period_start TIMESTAMP WITH TIME ZONE NOT NULL,
    period_end   TIMESTAMP WITH TIME ZONE NOT NULL,
    quantity     BIGINT NOT NULL DEFAULT 0 CHECK (quantity >= 0),
    updated_at   TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (org_id, meter, period_start),
    CHECK (period_start < period_end)
);

CREATE TABLE usage_events (
    id                 UUID PRIMARY KEY,
    org_id             UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    meter              TEXT NOT NULL CHECK (meter ~ '^[a-z][a-z0-9_]{0,127}$'),
    quantity           BIGINT NOT NULL CHECK (quantity > 0),
    idempotency_key    TEXT NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 255),
    request_hash       BYTEA NOT NULL CHECK (octet_length(request_hash) = 32),
    accepted           BOOLEAN NOT NULL,
    occurred_at        TIMESTAMP WITH TIME ZONE NOT NULL,
    period_start       TIMESTAMP WITH TIME ZONE NOT NULL,
    period_end         TIMESTAMP WITH TIME ZONE NOT NULL,
    dimensions         JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(dimensions) = 'object'),
    limit_at_ingestion BIGINT NOT NULL CHECK (limit_at_ingestion >= -1),
    total_after        BIGINT NOT NULL CHECK (total_after >= 0),
    created_at         TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (org_id, idempotency_key),
    CHECK (period_start < period_end),
    CHECK (occurred_at >= period_start AND occurred_at < period_end)
);

CREATE INDEX idx_usage_totals_org_period
    ON usage_totals (org_id, period_start, period_end);
CREATE INDEX idx_usage_events_org_meter_period
    ON usage_events (org_id, meter, period_start, occurred_at DESC);

-- Refuse to discard any non-canonical legacy period during the one-time
-- aggregate migration.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM usage_records
        WHERE period !~ '^[0-9]{4}-(0[1-9]|1[0-2])$'
    ) THEN
        RAISE EXCEPTION 'usage_records contains a non-YYYY-MM period';
    END IF;
    IF EXISTS (
        SELECT 1 FROM usage_records
        WHERE feature !~ '^[a-z][a-z0-9_]{0,127}$'
    ) THEN
        RAISE EXCEPTION 'usage_records contains a non-canonical meter key';
    END IF;
    IF EXISTS (SELECT 1 FROM usage_records WHERE quantity < 0) THEN
        RAISE EXCEPTION 'usage_records contains a negative quantity';
    END IF;
END $$;

-- The legacy table had no organization foreign key, so deleting a tenant
-- could strand an unusable aggregate. Those rows have no valid tenant owner
-- and cannot be retained in the new referentially safe schema.
DELETE FROM usage_records legacy
WHERE NOT EXISTS (SELECT 1 FROM organizations org WHERE org.id = legacy.org_id);

INSERT INTO usage_totals (org_id, meter, period_start, period_end, quantity, updated_at)
SELECT
    org_id,
    feature,
    to_date(period || '-01', 'YYYY-MM-DD')::timestamp AT TIME ZONE 'UTC',
    (to_date(period || '-01', 'YYYY-MM-DD') + INTERVAL '1 month')::timestamp AT TIME ZONE 'UTC',
    quantity,
    updated_at
FROM usage_records;

DROP TABLE usage_records;

ALTER TABLE usage_totals ENABLE ROW LEVEL SECURITY;
ALTER TABLE usage_totals FORCE ROW LEVEL SECURITY;
CREATE POLICY usage_totals_tenant ON usage_totals
    USING (org_id::text = current_setting('app.current_org_id', true))
    WITH CHECK (org_id::text = current_setting('app.current_org_id', true));

ALTER TABLE usage_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE usage_events FORCE ROW LEVEL SECURITY;
CREATE POLICY usage_events_tenant ON usage_events
    USING (org_id::text = current_setting('app.current_org_id', true))
    WITH CHECK (org_id::text = current_setting('app.current_org_id', true));

-- Override the broad transitional default privileges for these tables. Tenant
-- request code may append events and maintain totals, but never mutate history.
REVOKE ALL ON usage_events, usage_totals FROM app_tenant;
GRANT SELECT, INSERT ON usage_events TO app_tenant;
GRANT SELECT, INSERT, UPDATE ON usage_totals TO app_tenant;
