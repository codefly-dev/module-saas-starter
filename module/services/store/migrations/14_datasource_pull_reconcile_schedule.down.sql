-- Reverting puts the pull providers back outside the reconcile sweep, which is
-- what the code before this change expected: it never read their schedule, and
-- the due-set query filtered them out regardless of it.
--
-- It clears the schedule for every non-GitHub source rather than only the rows
-- this migration wrote, because nothing distinguishes them afterwards — a source
-- connected after the migration carries the same interval and a schedule of its
-- own. Down is therefore "no pull source is scheduled", the state the previous
-- code enforced in the query, not a row-for-row undo. reconcile_interval is left
-- where it is: the column is NOT NULL with its own default and no reader
-- consults it while next_reconcile_at is NULL.
ALTER TABLE public.datasource_sources NO FORCE ROW LEVEL SECURITY;

UPDATE public.datasource_sources
   SET next_reconcile_at = NULL,
       updated_at        = NOW()
 WHERE provider <> 'github';

ALTER TABLE public.datasource_sources FORCE ROW LEVEL SECURITY;
