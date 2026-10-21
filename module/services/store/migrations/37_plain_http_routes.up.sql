-- In-cell plain-HTTP cutover (#1118): nothing a running solution reports is
-- stored any more.
--
-- The reported backend half — the origin a solution's runtime served at,
-- reported at mint, and the alias and contract version beside it — is deleted.
-- A solution is reached where the Codefly SDK resolves the endpoint its applied
-- presence declares, exactly as a module is, and that address lives in no
-- record. The frontend half's contract version goes with it: it was compared
-- against the backend's, and nothing is reported to compare. The frontend half
-- itself stays and changes meaning: `frontend_manifest` is now the `frontend`
-- block of the applied presence, copied by the reconciler in the transaction
-- that applies the generation. Every value the runtime wrote there is cleared
-- below: a manifest a process reported must never read as a block delivery
-- declared, and the next applied generation writes the block.

UPDATE public.solution_registrations
SET frontend_revision = NULL, frontend_manifest = NULL,
    revision = nextval('public.solution_registry_revision_sequence'),
    updated_at = CURRENT_TIMESTAMP
WHERE frontend_revision IS NOT NULL;

ALTER TABLE public.solution_registrations
    DROP CONSTRAINT solution_registrations_frontend_half_whole,
    DROP CONSTRAINT solution_registrations_backend_half_whole,
    DROP CONSTRAINT solution_registrations_tombstone_is_empty,
    DROP CONSTRAINT solution_registrations_backend_revision_check,
    DROP COLUMN frontend_contract_version,
    DROP COLUMN backend_revision,
    DROP COLUMN backend_upstream,
    DROP COLUMN backend_service_alias,
    DROP COLUMN backend_contract_version,
    ADD CONSTRAINT solution_registrations_frontend_half_whole
        CHECK (num_nonnulls(frontend_revision, frontend_manifest) IN (0, 2)),
    ADD CONSTRAINT solution_registrations_tombstone_is_empty
        CHECK (tombstoned_at IS NULL OR frontend_revision IS NULL);
