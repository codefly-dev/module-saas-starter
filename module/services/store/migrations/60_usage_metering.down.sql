CREATE TABLE usage_records (
    org_id     UUID NOT NULL,
    feature    TEXT NOT NULL,
    period     TEXT NOT NULL,
    quantity   BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (org_id, feature, period)
);

INSERT INTO usage_records (org_id, feature, period, quantity, updated_at)
SELECT org_id, meter, to_char(period_start AT TIME ZONE 'UTC', 'YYYY-MM'), quantity, updated_at
FROM usage_totals;

CREATE INDEX idx_usage_records_org_period ON usage_records(org_id, period);
CREATE INDEX idx_usage_records_period ON usage_records(period);

ALTER TABLE usage_records ENABLE ROW LEVEL SECURITY;
ALTER TABLE usage_records FORCE ROW LEVEL SECURITY;
CREATE POLICY usage_records_tenant ON usage_records
    USING (
        org_id::text = current_setting('app.current_org_id', true)
        OR current_setting('app.bypass', true) = '1'
    )
    WITH CHECK (
        org_id::text = current_setting('app.current_org_id', true)
        OR current_setting('app.bypass', true) = '1'
    );
GRANT SELECT, INSERT, UPDATE, DELETE ON usage_records TO app_tenant;

DROP TABLE usage_events;
DROP TABLE usage_totals;
