-- Dropping the table would erase the record of every ceiling a platform
-- administrator wrote. The host would then grant nothing — the fail-closed
-- direction — but the history of who allowed what would be gone with nothing
-- left to say why, so a database holding any revision refuses the rollback.
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM public.module_envelopes) THEN
  RAISE EXCEPTION 'cannot remove the module envelope while any revision has been written';
 END IF;
END $$;
DROP TABLE public.module_envelopes;
