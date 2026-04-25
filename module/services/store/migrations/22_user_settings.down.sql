DROP INDEX IF EXISTS idx_users_settings_gin;
ALTER TABLE users DROP COLUMN IF EXISTS settings;
