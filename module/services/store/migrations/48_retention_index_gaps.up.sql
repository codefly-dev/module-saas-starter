-- =============================================================================
-- Migration 48: retention index gaps (#1253)
-- =============================================================================
--
-- Scheduled retention deletes were quadratic: two ON DELETE CASCADE foreign
-- keys on bulk-deleted parent tables had no index on the child side, so the
-- cascade trigger sequentially scanned the child once per deleted parent row.
-- Measured on 300k seeded rows per table: DELETE FROM mfa_login_transactions
-- WHERE expires_at < $1 (1,001 rows) ran 20,199ms with a cascade into
-- webauthn_ceremonies via its unindexed mfa_login_transaction_id column, 56ms
-- after the index below (359x); DELETE FROM sessions WHERE created_at < $1
-- (1,001 rows) ran 15,697ms with no usable index on created_at at all, 38ms
-- after the index below (418x).

-- ---------------------------------------------------------------------------
-- 1. CASCADE targets of bulk retention deletes
--    rows_deleted x child_table_size without these: the cascade trigger fires
--    once per deleted parent row and sequentially scans the child table each
--    time it has no index on the FK column.
-- ---------------------------------------------------------------------------

-- DeleteExpiredAuthenticationCeremonies: DELETE FROM mfa_login_transactions
-- WHERE expires_at < $1 cascades into webauthn_ceremonies. 359x above.
CREATE INDEX idx_webauthn_ceremonies_mfa_tx
    ON public.webauthn_ceremonies (mfa_login_transaction_id);

-- DeleteExpiredAuthenticationCeremonies: client_authorization_codes cascades
-- from both sessions (session_id) and users (user_id). 418x-class gain; the
-- session_id side is the one measured as sessions is the bulk-deleted parent.
CREATE INDEX idx_client_authorization_codes_session
    ON public.client_authorization_codes (session_id);
CREATE INDEX idx_client_authorization_codes_user
    ON public.client_authorization_codes (user_id);

-- ---------------------------------------------------------------------------
-- 2. Retention sweep predicates with no usable leading key
--    Each sweep deletes WHERE created_at < $1 but the existing indexes on
--    these tables lead with a different column (or have none on created_at
--    at all), so the planner has no choice but a full sequential scan.
-- ---------------------------------------------------------------------------

-- DeleteOldSessions: DELETE FROM sessions WHERE created_at < $1. 418x above.
-- Existing sessions indexes are all on expires_at / user_id / family_id.
CREATE INDEX idx_sessions_created_at
    ON public.sessions (created_at);

-- DeleteOldNotifications: DELETE FROM notifications WHERE created_at < $1.
-- idx_notifications_user leads with user_id, not created_at.
CREATE INDEX idx_notifications_created_at
    ON public.notifications (created_at);

-- DeleteOldWebhookDeliveries: DELETE FROM webhook_deliveries WHERE
-- created_at < $1. idx_webhook_deliveries_sub leads with subscription_id.
CREATE INDEX idx_webhook_deliveries_created_at
    ON public.webhook_deliveries (created_at);

-- ---------------------------------------------------------------------------
-- 3. Role deletion cascade children
--    DELETE FROM roles WHERE id = $1 fires a cascade trigger on each of
--    these; structural from the FK, unmeasured (seeding roles/scope_grants/
--    record_shares hit CHECK constraints before a representative row count
--    could be reached) — say so rather than claim a factor.
-- ---------------------------------------------------------------------------

CREATE INDEX idx_role_assignments_role
    ON public.role_assignments (role_id);
CREATE INDEX idx_scope_grants_role
    ON public.scope_grants (role_id);
CREATE INDEX idx_record_shares_role
    ON public.record_shares (role_id);

-- ---------------------------------------------------------------------------
-- 4. Exact duplicate index
--    idx_sessions_active and idx_sessions_expires_at are byte-identical
--    (both ON sessions(expires_at) WHERE revoked_at IS NULL), carried into
--    1_baseline.up.sql from migrations 6 and 11. Pure write amplification on
--    session creation; idx_sessions_expires_at serves no query that
--    idx_sessions_active does not already cover, and the cleanup job its name
--    suggests filters created_at, not expires_at.
-- ---------------------------------------------------------------------------

DROP INDEX IF EXISTS public.idx_sessions_expires_at;
