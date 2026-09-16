DROP INDEX IF EXISTS public.idx_sessions_open_impersonation;

-- Restoring NOT NULL means removing the rows that carry no refresh credential.
-- sessions FORCEs row-level security and its policy reads app.current_user_id,
-- which a migration does not set, so without suspending FORCE the DELETE would
-- match zero rows, report success, and leave the ALTER below to fail on the
-- rows it was meant to have removed. Suspending FORCE is owner-only and
-- restored before this migration ends; it needs no BYPASSRLS attribute.
--
-- The deleted rows are impersonation windows, capped at minutes and holding no
-- credential anyone can present. Rolling back discards open support sessions,
-- not logins.
ALTER TABLE public.sessions NO FORCE ROW LEVEL SECURITY;

DELETE FROM public.sessions WHERE refresh_token_hash IS NULL;

ALTER TABLE public.sessions FORCE ROW LEVEL SECURITY;

ALTER TABLE public.sessions
 DROP CONSTRAINT IF EXISTS sessions_impersonation_has_no_refresh_credential,
 DROP COLUMN IF EXISTS acting_as_user_id,
 ALTER COLUMN refresh_token_hash SET NOT NULL;
