CREATE SCHEMA IF NOT EXISTS measurement;

CREATE TABLE IF NOT EXISTS measurement.metric_values (
    metric_key text NOT NULL,
    observed_at timestamptz NOT NULL,
    value numeric NOT NULL,
    dimensions jsonb NOT NULL DEFAULT '{}'::jsonb,
    source_fresh_at timestamptz NOT NULL,
    PRIMARY KEY (metric_key, observed_at, dimensions)
);
