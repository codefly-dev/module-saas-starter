-- User-level preferences. JSONB rather than a wide schema so future
-- additions (a new `default_org_id`, a flag for a beta feature, an
-- accessibility toggle) don't need migrations + handler updates +
-- proto codegen each time. Frontend writes a partial JSON; the api
-- merges it on the column with jsonb || (concatenation, last-write
-- wins per top-level key).
--
-- Default '{}'::jsonb so every user has a non-NULL settings value
-- on the first read — the application code never needs to handle
-- "settings is missing", just "key is missing".

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS settings JSONB NOT NULL DEFAULT '{}'::jsonb;

-- GIN index lets jsonb-key queries (e.g. "list all users with
-- email.marketing=true for a campaign") stay sub-second on large
-- tables. Cheap to maintain on small JSON; pays off the first time
-- you need it.
CREATE INDEX IF NOT EXISTS idx_users_settings_gin
    ON users USING GIN (settings);
