-- Dropping the table removes its policy, index, and CHECK constraint with it.
-- The shared settings_jsonb_* functions belong to migration 80 and stay.
DROP TABLE IF EXISTS org_generic_settings;
