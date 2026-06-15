DROP INDEX IF EXISTS idx_teams_org_path;
DROP INDEX IF EXISTS idx_teams_parent;
ALTER TABLE teams DROP CONSTRAINT IF EXISTS teams_path_depth;
ALTER TABLE teams DROP CONSTRAINT IF EXISTS teams_slug_shape;
ALTER TABLE teams DROP CONSTRAINT IF EXISTS teams_org_id_path_key;
ALTER TABLE teams ADD CONSTRAINT teams_org_id_name_key UNIQUE (org_id, name);
ALTER TABLE teams DROP COLUMN IF EXISTS path;
ALTER TABLE teams DROP COLUMN IF EXISTS slug;
ALTER TABLE teams DROP COLUMN IF EXISTS parent_team_id;
