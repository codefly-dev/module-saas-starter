SET LOCAL lock_timeout = '15s';
DELETE FROM public.sessions WHERE role_view;
ALTER TABLE public.sessions DROP CONSTRAINT sessions_access_only_has_no_refresh_credential;
ALTER TABLE public.sessions ADD CONSTRAINT sessions_impersonation_has_no_refresh_credential
    CHECK (((refresh_token_hash IS NULL) = (acting_as_user_id IS NOT NULL)));
ALTER TABLE public.sessions DROP COLUMN role_view;
