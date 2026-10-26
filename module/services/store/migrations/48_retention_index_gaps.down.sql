-- Migration 48 down: retention index gaps (#1253)

CREATE INDEX idx_sessions_expires_at
    ON public.sessions (expires_at)
    WHERE revoked_at IS NULL;

DROP INDEX IF EXISTS public.idx_record_shares_role;
DROP INDEX IF EXISTS public.idx_scope_grants_role;
DROP INDEX IF EXISTS public.idx_role_assignments_role;

DROP INDEX IF EXISTS public.idx_webhook_deliveries_created_at;
DROP INDEX IF EXISTS public.idx_notifications_created_at;
DROP INDEX IF EXISTS public.idx_sessions_created_at;

DROP INDEX IF EXISTS public.idx_client_authorization_codes_user;
DROP INDEX IF EXISTS public.idx_client_authorization_codes_session;
DROP INDEX IF EXISTS public.idx_webauthn_ceremonies_mfa_tx;
