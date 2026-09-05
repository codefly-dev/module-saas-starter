-- Reverting to the ('github', 'api') shape assumes no 'crawler' or 'upload' rows
-- remain; they would violate the restored CHECK. Drop them first if rolling back
-- with such sources connected.
ALTER TABLE datasource_sources
    DROP CONSTRAINT datasource_sources_provider_check,
    ADD  CONSTRAINT datasource_sources_provider_check
             CHECK (provider IN ('github', 'api'));
