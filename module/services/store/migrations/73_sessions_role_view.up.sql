-- A role view is a session that is never refreshed: its token is its whole life.
-- Like an impersonation window it holds no refresh credential, and it is told
-- apart by its own column, not by a window's acting_as_user_id, because it acts
-- as the person who holds it.
ALTER TABLE public.sessions ADD COLUMN role_view boolean NOT NULL DEFAULT false;
ALTER TABLE public.sessions DROP CONSTRAINT sessions_impersonation_has_no_refresh_credential;
ALTER TABLE public.sessions ADD CONSTRAINT sessions_access_only_has_no_refresh_credential
    CHECK (((refresh_token_hash IS NULL) = ((acting_as_user_id IS NOT NULL) OR role_view)));
