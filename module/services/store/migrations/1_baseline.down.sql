-- The ledger is one baseline and carries no downgrade: recreate the database instead.
DO $$ BEGIN RAISE EXCEPTION 'the store baseline is forward-only; recreate the database'; END $$;
