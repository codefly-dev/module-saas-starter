-- Phase 2H — RLS on sessions.
--
-- Session rows are user-scoped (refresh tokens belong to ONE user).
-- The existing WHERE user_id = $1 SQL filter is the primary safety
-- property; this RLS layer is symmetric defense-in-depth, parallel
-- to the Phase 2G policies on notifications / mfa_devices /
-- mfa_backup_codes.
--
-- Cross-user access patterns (refresh-token-hash lookup at login,
-- family-rotation revocation, platform-admin "list sessions for
-- user X") run under WithBypass — see pkg/auth/pg/session_store.go.
--
-- The pkg/auth/pg/SessionStore was refactored alongside this
-- migration to take an RLSWrapper (PostgresStore in production;
-- WithUserTx-stamped helper in tests) so the per-method wrap is
-- explicit at every call site. See AUTHZ.md.

ALTER TABLE sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE sessions FORCE  ROW LEVEL SECURITY;

CREATE POLICY sessions_user ON sessions
    USING (
        current_setting('app.bypass', true) = '1'
        OR user_id::text = current_setting('app.current_user_id', true)
    )
    WITH CHECK (
        current_setting('app.bypass', true) = '1'
        OR user_id::text = current_setting('app.current_user_id', true)
    );
