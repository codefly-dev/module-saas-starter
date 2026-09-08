-- Reverse 117_datasource_source_ordinal.

ALTER TABLE datasource_sources
    DROP COLUMN IF EXISTS next_ordinal;
