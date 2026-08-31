-- Persist the session user's presentational identity (email + display name) so
-- a rotated access token — minted on refresh or org-switch without a fresh
-- provider login — reissues the same `email`/`name` claims a client renders,
-- and so the durable MFA login handoff carries them across the second factor.
-- Both are display-only and never an authentication or authorization input.

ALTER TABLE sessions
    ADD COLUMN email TEXT,
    ADD COLUMN display_name TEXT;

ALTER TABLE mfa_login_transactions
    ADD COLUMN email TEXT,
    ADD COLUMN display_name TEXT;
