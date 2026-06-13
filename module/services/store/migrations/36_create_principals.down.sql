-- Drop everything 36_create_principals.up.sql added. Indexes drop
-- automatically with the table; explicit DROP INDEX kept for clarity.
DROP INDEX IF EXISTS principals_display_name_idx;
DROP INDEX IF EXISTS principals_agent_identifier_org_idx;
DROP INDEX IF EXISTS principals_org_kind_idx;
DROP TABLE IF EXISTS "principals";
