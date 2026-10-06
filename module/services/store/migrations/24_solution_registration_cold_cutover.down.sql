-- Irreversible, but for a narrower reason than before.
--
-- The up migration no longer destroys rows: a runtime-registered registration
-- is WITHDRAWN and keeps its `runtime_boundary` seed, so identity and the
-- reachability of the runs it admitted both survive. What it does destroy is
-- the runtime-owned OBSERVATIONS — manifest, upstream, service alias, contract
-- versions and the two lease expiry columns, which are dropped outright.
-- Recreating empty columns would not restore those values or their provenance.
--
-- Restore a pre-cutover backup with its matching application release to recover
-- the observations. Identity does not need recovering: the seeds were never
-- touched.
DO $$ BEGIN
    RAISE EXCEPTION 'migration 24 is irreversible: the runtime lease and observation columns are dropped and cannot be recovered (identity is unaffected — runtime_boundary seeds are preserved by the up migration)';
END $$;
