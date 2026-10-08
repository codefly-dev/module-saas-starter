-- codefly:irreversible the seeds are destroyed outright. Re-adding the column
-- draws FRESH `gen_random_uuid()` values, which is not a restoration: the old
-- seeds were the only input their boundaries had, so a run still executing under
-- a boundary derived from one becomes unreachable from any page. Restore a
-- pre-drop backup with its matching application release to recover them.
--
-- The marker above is load-bearing, not decoration. The managed-database
-- qualification rolls the ledger back from the head and must stop somewhere: it
-- reads this marker to find the newest DECLARED-irreversible version, asserts
-- every migration above it reverses cleanly, requires the ledger to come to rest
-- exactly on it, and asserts one further step is refused by the frontier's own
-- raise. A down file that raises WITHOUT the marker is still a failure, which is
-- what keeps "irreversible" a decision somebody wrote down rather than a
-- rollback that happened to break. This file moves the frontier from 26 to 28
-- by the gate's newest-declared-marker rule; migration 26 keeps its own marker,
-- because the frontier is derived and not written down twice.
--
-- An empty column would be worse than a refusal: with the seeds gone nothing can
-- tell a re-defaulted value from the one a run was filed under, and the mint
-- would seal capabilities under boundaries that name nothing. Code at this
-- version reads `declared_binding_id` instead, so a restored column would also
-- be dead on arrival.
DO $$ BEGIN
    RAISE EXCEPTION 'migration 28 is irreversible: the per-registration runtime_boundary seeds are dropped and cannot be recovered (the boundary is derived from the delivered presence binding at this version — restore a pre-drop backup to recover the old seeds)';
END $$;
