-- Reverting to the GitHub-only shape assumes no 'api' rows remain (they have a
-- NULL repo and would violate the restored NOT NULL / provider CHECK); drop them
-- first if rolling back with API sources connected.
ALTER TABLE datasource_sources
    DROP COLUMN IF EXISTS config,
    ALTER COLUMN repo SET NOT NULL,
    DROP CONSTRAINT datasource_sources_provider_check,
    ADD  CONSTRAINT datasource_sources_provider_check
             CHECK (provider IN ('github'));
