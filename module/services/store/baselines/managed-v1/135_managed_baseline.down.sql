DO $$ BEGIN RAISE EXCEPTION 'managed baseline is forward-only; restore a reviewed backup or apply a forward fix'; END $$;
