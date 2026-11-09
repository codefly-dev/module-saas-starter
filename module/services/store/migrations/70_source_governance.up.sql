-- A source a solution's page connects belongs to that solution's installation,
-- and is governed by one scope of the solution's own declaration: connecting
-- and changing it require that the authority issue the caller that scope at the
-- installation, and nothing else (business.SourceGovernance). The tie is set
-- once at connect and never moves, so these columns are written by the insert
-- alone; all are NULL for an organization's own source, which is every source
-- that exists when this migration runs. Nothing is filled: no recorded value
-- says which existing source a solution connected, and a guess would hand a
-- source to a role nobody chose.
SET LOCAL lock_timeout = '15s';
ALTER TABLE public.datasource_sources
    ADD COLUMN governing_installation_id text,
    ADD COLUMN governing_kind text,
    ADD COLUMN governing_action text,
    ADD COLUMN governing_binding text,
    ADD CONSTRAINT datasource_sources_governance_check CHECK (
        (governing_installation_id IS NULL AND governing_kind IS NULL AND governing_action IS NULL AND governing_binding IS NULL)
        OR (governing_installation_id <> '' AND governing_kind <> '' AND governing_action <> '')
    );
