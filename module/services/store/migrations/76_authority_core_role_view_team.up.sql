-- A cast is a role view that names a team: the session acts as a member of
-- that team and of nothing else (authority core, OpenTeamCast). The core
-- writes the team on the same record a view is written on, and this table had
-- nowhere to keep it: a cast written here would have read back as a view that
-- narrows nothing, which is the person's own reach, the opposite of what was
-- asked. Empty for a view that is not a cast. Written once with the row, like
-- every other column: the table still grants no update and no delete.
SET LOCAL lock_timeout = '15s';
ALTER TABLE public.authority_core_role_views ADD COLUMN team_id text NOT NULL DEFAULT '';
