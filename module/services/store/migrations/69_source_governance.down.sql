-- Removes the tie: every source is the organization's own again, governed by
-- the organization rule alone.
SET LOCAL lock_timeout = '15s';
ALTER TABLE public.datasource_sources
    DROP CONSTRAINT datasource_sources_governance_check,
    DROP COLUMN governing_binding,
    DROP COLUMN governing_action,
    DROP COLUMN governing_kind,
    DROP COLUMN governing_installation_id;
