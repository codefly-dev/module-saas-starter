-- Per-user consent acceptance — augments the FE-only ConsentBanner
-- with a server-side audit trail. Required for SOC2 / GDPR-Article-7
-- ("demonstrate consent was given") and for any regulated industry
-- vertical that asks for proof of acceptance.
--
-- Two columns on users (idempotent — column-level migrations don't
-- block running services):
--   terms_version       — the CONSENT_VERSION string the user
--                         clicked Accept on. NULL until first accept.
--   terms_accepted_at   — wall-clock of acceptance. NULL until first.
--
-- The version string is whatever CONSENT_VERSION the FE was rendering
-- when the click happened. When the operator bumps that constant
-- (after a TOS / privacy policy change), every user's row goes stale
-- relative to the NEW version, the banner returns, and a fresh
-- accept records the new (version, timestamp) pair.

ALTER TABLE users ADD COLUMN IF NOT EXISTS terms_version TEXT;
ALTER TABLE users ADD COLUMN IF NOT EXISTS terms_accepted_at TIMESTAMPTZ;

-- Optional history table — uncomment if your compliance regime needs
-- "show me every version every user has ever accepted" rather than
-- just "what's the current state". Keeps users.terms_* as a fast
-- lookup denorm.
--
-- CREATE TABLE IF NOT EXISTS user_consent_acceptances (
--     id UUID PRIMARY KEY,
--     user_id UUID NOT NULL REFERENCES users(uuid) ON DELETE CASCADE,
--     terms_version TEXT NOT NULL,
--     accepted_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
--     ip_address INET,
--     user_agent TEXT
-- );
