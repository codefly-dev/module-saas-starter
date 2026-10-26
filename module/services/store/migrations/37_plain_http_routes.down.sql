-- Reverses the schema only. The reported halves cannot be recovered: nothing
-- reports them any more, and the frontend blocks written by the reconciler are
-- cleared so the restored columns read as the old shape (absent halves), never
-- as a block mistaken for a runtime-reported manifest.

UPDATE public.solution_registrations
SET frontend_revision = NULL, frontend_manifest = NULL,
    revision = nextval('public.solution_registry_revision_sequence'),
    updated_at = CURRENT_TIMESTAMP
WHERE frontend_revision IS NOT NULL;

ALTER TABLE public.solution_registrations
    DROP CONSTRAINT solution_registrations_frontend_half_whole,
    DROP CONSTRAINT solution_registrations_tombstone_is_empty,
    ADD COLUMN frontend_contract_version text,
    ADD COLUMN backend_revision bigint,
    ADD COLUMN backend_upstream text,
    ADD COLUMN backend_service_alias text,
    ADD COLUMN backend_contract_version text,
    ADD CONSTRAINT solution_registrations_backend_revision_check CHECK (backend_revision > 0),
    ADD CONSTRAINT solution_registrations_frontend_half_whole
        CHECK (num_nonnulls(frontend_revision, frontend_manifest) IN (0, 2)),
    ADD CONSTRAINT solution_registrations_backend_half_whole
        CHECK (num_nonnulls(backend_revision, backend_upstream, backend_service_alias) IN (0, 3)),
    ADD CONSTRAINT solution_registrations_tombstone_is_empty
        CHECK (tombstoned_at IS NULL OR num_nonnulls(frontend_revision, backend_revision) = 0);
