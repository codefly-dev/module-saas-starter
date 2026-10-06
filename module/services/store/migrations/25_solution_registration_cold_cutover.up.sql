-- Cold cutover: the runtime registration writer is gone, so its OBSERVATIONS
-- are discarded — but a row is never destroyed, and a seed is never regenerated.
--
-- WHY A WITHDRAWAL RATHER THAN A DELETE. A runtime-registered row with no
-- declared binding is a solution the new registry has not AUTHORIZED. That is a
-- registry STATE, not a row to destroy. Deleting it would take
-- `runtime_boundary` with it, and a later approval through the declared-presence
-- path would mint a fresh `gen_random_uuid()` seed — so every run that solution
-- had already admitted becomes unreachable, because a run is reachable only
-- under the boundary it was admitted with. Withdrawing the row keeps the seed,
-- so approval reactivates the SAME boundary and those runs stay reachable.
--
-- THE SEED IS NEVER TOUCHED HERE. `runtime_boundary` appears in no statement
-- below. That is the same discipline the save path keeps — the column simply
-- never appears on a write — and it is why re-approval cannot change it.

-- The seed must exist before anything is withdrawn. Migration 17 added the
-- column NOT NULL with a `gen_random_uuid()` default, so a NULL here is
-- impossible; it is checked anyway because the alternative to surfacing a
-- defect is papering over it, and this one would silently cost a solution its
-- runs. A migration that refuses is recoverable; a regenerated seed is not.
DO $$
DECLARE unseeded bigint;
BEGIN
    SELECT count(*) INTO unseeded
      FROM public.solution_registrations
     WHERE declared_binding_id IS NULL AND runtime_boundary IS NULL;
    IF unseeded > 0 THEN
        RAISE EXCEPTION
            'cold cutover refuses: % registration(s) would be withdrawn with no runtime_boundary seed; '
            'migration 17 makes that impossible, so this is a defect to investigate rather than to migrate past',
            unseeded;
    END IF;
END $$;

-- Withdraw every runtime-registered row: it is tombstoned, its observations are
-- cleared, and its seed is left exactly as it is.
UPDATE public.solution_registrations
SET frontend_revision = NULL, frontend_manifest = NULL,
    frontend_contract_version = NULL, frontend_lease_expires_at = NULL,
    backend_revision = NULL, backend_upstream = NULL, backend_service_alias = NULL,
    backend_contract_version = NULL, backend_lease_expires_at = NULL,
    tombstoned_at = COALESCE(tombstoned_at, CURRENT_TIMESTAMP),
    revision = nextval('public.solution_registry_revision_sequence'),
    updated_at = CURRENT_TIMESTAMP
WHERE declared_binding_id IS NULL;

-- And clear the observations the runtime path wrote on rows that ARE declared.
-- The declared path rewrites them; a runtime's old endpoint or manifest must
-- never read as an observation delivery approved.
UPDATE public.solution_registrations
SET frontend_revision = NULL, frontend_manifest = NULL,
    frontend_contract_version = NULL, frontend_lease_expires_at = NULL,
    backend_revision = NULL, backend_upstream = NULL, backend_service_alias = NULL,
    backend_contract_version = NULL, backend_lease_expires_at = NULL,
    revision = nextval('public.solution_registry_revision_sequence'),
    updated_at = CURRENT_TIMESTAMP
WHERE declared_binding_id IS NOT NULL
  AND (frontend_revision IS NOT NULL OR backend_revision IS NOT NULL);

-- Dropping the columns also drops any indexes dependent on them. No lease-only
-- table or standalone lease index exists in migrations 1..23.
--
-- THE DECLARED COLUMNS STAY NULLABLE, and that is a consequence of withdrawing
-- rather than deleting. This migration used to `SET NOT NULL` on all four,
-- which is only possible once every undeclared row is gone — so not setting
-- them is the whole change here.
--
-- Nothing replaces those NOT NULLs, because the invariant already exists:
-- migration 22 defines `solution_registrations_declared_whole` as
-- `num_nonnulls(...) = ANY (ARRAY[0, 4])`, which is exactly the state machine
-- this needs — wholly declared (authorized) or wholly undeclared (withdrawn,
-- awaiting approval), never three-of-four. An earlier draft of this migration
-- added that constraint a second time and the replay refused it as already
-- existing, which is the right answer: the schema already said it.
ALTER TABLE public.solution_registrations
    DROP CONSTRAINT solution_registrations_frontend_half_whole,
    DROP CONSTRAINT solution_registrations_backend_half_whole,
    DROP COLUMN frontend_lease_expires_at,
    DROP COLUMN backend_lease_expires_at,
    ADD CONSTRAINT solution_registrations_frontend_half_whole
        CHECK (num_nonnulls(frontend_revision, frontend_manifest) IN (0, 2)),
    ADD CONSTRAINT solution_registrations_backend_half_whole
        CHECK (num_nonnulls(backend_revision, backend_upstream, backend_service_alias) IN (0, 3));

-- An undeclared row must also be a withdrawn one: it is not authorized, so it
-- must not read as serving. Enforced rather than left to the writer, because
-- the whole point of the withdrawal above is that the state is durable.
ALTER TABLE public.solution_registrations
    ADD CONSTRAINT solution_registrations_undeclared_is_withdrawn
        CHECK (declared_binding_id IS NOT NULL OR tombstoned_at IS NOT NULL);
