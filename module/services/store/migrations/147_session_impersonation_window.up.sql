-- An impersonation window is a session that holds no rotatable credential. The
-- minter signs its access token and persists the row, but generates no refresh
-- token at all, so there is nothing to store and nothing an attacker could
-- present at /auth/refresh. Dropping NOT NULL is what lets the schema carry
-- that absence instead of a convention: `refresh_token_hash = $1` never matches
-- NULL, so the rotation path cannot reach an impersonation row even with a
-- forged or replayed hash.
--
-- acting_as_user_id names the impersonated user. It separates an impersonation
-- window from the admin's own logins when their sessions are listed, and makes
-- "which impersonation windows are open" an indexed question.
--
-- Classification (DATABASE_AUTHORITY.md): no new relation and no change to the
-- sessions boundary. The row stays user-scoped by user_id — the real actor,
-- whose device list it belongs in — under the existing sessions_user policy and
-- app_tenant grants, which are relation-wide and already cover a new column.

ALTER TABLE public.sessions
 ADD COLUMN acting_as_user_id UUID REFERENCES public.users(uuid) ON DELETE CASCADE,
 ALTER COLUMN refresh_token_hash DROP NOT NULL,
 ADD CONSTRAINT sessions_impersonation_has_no_refresh_credential CHECK (
  (refresh_token_hash IS NULL) = (acting_as_user_id IS NOT NULL)
 );

CREATE INDEX idx_sessions_open_impersonation
 ON public.sessions (acting_as_user_id, user_id)
 WHERE acting_as_user_id IS NOT NULL AND revoked_at IS NULL;
