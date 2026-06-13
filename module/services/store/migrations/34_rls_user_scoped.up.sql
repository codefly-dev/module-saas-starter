-- Phase 2G — RLS on user-scoped tables.
--
-- Tables here aren't tenant-scoped (no org_id) — they belong to ONE
-- user. The existing WHERE user_id = $1 SQL filter is the primary
-- isolation; this RLS layer is symmetric defense-in-depth (same
-- spirit as Phase 1-2F, just scoped to a user instead of an org).
--
-- Mechanism: same as WithOrgTx, but using a parallel GUC
-- `app.current_user_id`. Set via infra.WithUserTx(ctx, userID, fn).
--
-- Cross-user readers (platform admin "list sessions for any user",
-- refresh-token-hash lookup during login) use WithBypass — same
-- bypass GUC as the org-RLS path.
--
-- Tables covered:
--   * notifications — per-user inbox (org_id is descriptive, not
--     authoritative; user_id is the access key).
--   * mfa_devices — TOTP / WebAuthn enrollments.
--   * mfa_backup_codes — one-shot recovery codes.
--
-- Skipped (intentional):
--   * sessions — auth/pg/SessionStore is a separate package using
--     the pool directly and has cross-user readers (refresh-token-
--     hash lookup, family revocation). Adding RLS here requires
--     refactoring SessionStore to use the WithUserTx/WithBypass
--     helpers — tracked for a follow-up. The WHERE user_id = $1
--     SQL filter remains the primary safety property.
--   * magic_links — keyed by token_hash, not user_id; token IS
--     the secret. RLS on user_id wouldn't help.

DO $$
DECLARE
    t TEXT;
BEGIN
    FOREACH t IN ARRAY ARRAY[
        'notifications',
        'mfa_devices',
        'mfa_backup_codes'
    ] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE %I FORCE  ROW LEVEL SECURITY', t);
        EXECUTE format($f$
            CREATE POLICY %I_user ON %I
                USING (
                    current_setting('app.bypass', true) = '1'
                    OR user_id::text = current_setting('app.current_user_id', true)
                )
                WITH CHECK (
                    current_setting('app.bypass', true) = '1'
                    OR user_id::text = current_setting('app.current_user_id', true)
                )
        $f$, t, t);
    END LOOP;
END $$;
