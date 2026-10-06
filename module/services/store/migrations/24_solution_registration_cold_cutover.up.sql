-- Cold cutover: runtime registrations and their observations are discarded.
-- Keep declared presence, immutable target identity and tombstones. A runtime's
-- old endpoint/manifest must not become an observation approved by delivery.
DELETE FROM public.solution_registrations WHERE declared_binding_id IS NULL;

UPDATE public.solution_registrations
SET frontend_revision = NULL, frontend_manifest = NULL,
    frontend_contract_version = NULL, frontend_lease_expires_at = NULL,
    backend_revision = NULL, backend_upstream = NULL, backend_service_alias = NULL,
    backend_contract_version = NULL, backend_lease_expires_at = NULL,
    revision = nextval('public.solution_registry_revision_sequence'),
    updated_at = CURRENT_TIMESTAMP
WHERE frontend_revision IS NOT NULL OR backend_revision IS NOT NULL;

-- Dropping the columns also drops any indexes dependent on them. No lease-only
-- table or standalone lease index exists in migrations 1..22.
ALTER TABLE public.solution_registrations
    DROP CONSTRAINT solution_registrations_frontend_half_whole,
    DROP CONSTRAINT solution_registrations_backend_half_whole,
    DROP COLUMN frontend_lease_expires_at,
    DROP COLUMN backend_lease_expires_at,
    ALTER COLUMN declared_binding_id SET NOT NULL,
    ALTER COLUMN declared_generation SET NOT NULL,
    ALTER COLUMN declared_release SET NOT NULL,
    ALTER COLUMN declared_target_id SET NOT NULL,
    ADD CONSTRAINT solution_registrations_frontend_half_whole
        CHECK (num_nonnulls(frontend_revision, frontend_manifest) IN (0, 2)),
    ADD CONSTRAINT solution_registrations_backend_half_whole
        CHECK (num_nonnulls(backend_revision, backend_upstream, backend_service_alias) IN (0, 3));
