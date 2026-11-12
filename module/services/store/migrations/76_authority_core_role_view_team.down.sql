SET LOCAL lock_timeout = '15s';
-- A cast whose team is forgotten would act as its person. Its session ends
-- with the column instead; the session it was opened from is untouched.
DELETE FROM public.sessions s USING public.authority_core_role_views v
 WHERE v.team_id <> '' AND s.id::text = v.session_id;
DELETE FROM public.authority_core_role_views WHERE team_id <> '';
ALTER TABLE public.authority_core_role_views DROP COLUMN team_id;
