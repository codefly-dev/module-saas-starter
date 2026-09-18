-- The private custody surface is gone from the host: durable execution belongs
-- to the orchestration module, and the host carries no knowledge of it.
--
-- A grant that is still live means a consumer is still brokering through this
-- table, and dropping it mid-window would end that consumer's work silently. The
-- migration refuses instead; it applies once every grant has expired or been
-- erased. The table forces row-level security and its policies admit only the
-- request and control-plane roles, so the owner running this migration would
-- read zero rows through them — RLS is lifted first so the count is the truth.
ALTER TABLE public.execution_custody NO FORCE ROW LEVEL SECURITY;
DO $$
DECLARE
  live BIGINT;
BEGIN
  SELECT count(*) INTO live
  FROM public.execution_custody
  WHERE expires_at > CURRENT_TIMESTAMP AND envelope <> '';
  IF live > 0 THEN
    RAISE EXCEPTION USING
      MESSAGE = format('execution_custody still holds %s live grant(s); move their consumer off the retired custody surface and let them expire before applying this migration', live),
      ERRCODE = 'check_violation';
  END IF;
END
$$;
DROP TABLE public.execution_custody;
