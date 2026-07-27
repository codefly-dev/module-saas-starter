-- User-level preferences. PostgreSQL stores the canonical ProtoJSON projection
-- in JSONB, so adding a typed protobuf setting requires contract/code
-- generation but no SQL column migration. Application code never manipulates
-- raw JSON. Partial protobuf messages are merged under a user-row lock so
-- nested sibling settings and explicit scalar presence are preserved.
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
