-- Irreversible. Runtime-owned registrations, manifests, upstreams and lease
-- expiry timestamps were destroyed. Recreating empty columns would not restore
-- those observations or their provenance. Restore a pre-cutover database backup
-- with its matching application release to recover that state.
DO $$ BEGIN
    RAISE EXCEPTION 'migration 24 is irreversible: runtime registrations and lease observations cannot be recovered';
END $$;
