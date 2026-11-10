-- When an installation the shared authority core holds (a solution's, and a
-- module page's) was first written: what an administrator's listing shows as
-- its install date. A record written before this was kept has none, and the
-- listing says so rather than naming a date it does not know; a new one takes
-- the time of its first write, and later writes (activation, re-convergence,
-- an update) never move it.
SET LOCAL lock_timeout = '15s';
ALTER TABLE public.authority_core_installations ADD COLUMN installed_at timestamp with time zone;
ALTER TABLE public.authority_core_installations ALTER COLUMN installed_at SET DEFAULT now();
