DROP INDEX IF EXISTS idx_principals_created_by;
ALTER TABLE principals DROP COLUMN IF EXISTS created_by;
