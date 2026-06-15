-- Team hierarchy (Identity Claims v1, ask #1 — driven by a policy-enforcement consumer).
--
-- Teams become a strict tree: parent_team_id is the authoritative edge; path is
-- the materialized slug-path ('engineering/platform'), maintained on write by the
-- business layer. Plain Postgres on purpose: ancestors = path prefix split,
-- subtree = LIKE path || '/%' (ltree is an optional later upgrade; a graph
-- extension is deliberately avoided — not available on managed Postgres, and a
-- shallow tree doesn't need it).
--
-- Cycle safety (v1): teams are created under an existing parent and there is no
-- move/reparent RPC, so cycles cannot form; a reparent feature must add a guard.

ALTER TABLE teams ADD COLUMN parent_team_id UUID REFERENCES teams(id) ON DELETE CASCADE;
ALTER TABLE teams ADD COLUMN slug TEXT;
ALTER TABLE teams ADD COLUMN path TEXT;

-- Backfill existing flat teams: slug from name, path = slug (roots).
UPDATE teams SET
    slug = trim(both '-' from lower(regexp_replace(name, '[^a-zA-Z0-9]+', '-', 'g'))),
    path = trim(both '-' from lower(regexp_replace(name, '[^a-zA-Z0-9]+', '-', 'g')))
WHERE slug IS NULL;

ALTER TABLE teams ALTER COLUMN slug SET NOT NULL;
ALTER TABLE teams ALTER COLUMN path SET NOT NULL;

-- Identity is now the path (cousins may share a name; siblings may not share a slug).
ALTER TABLE teams DROP CONSTRAINT IF EXISTS teams_org_id_name_key;
ALTER TABLE teams ADD CONSTRAINT teams_org_id_path_key UNIQUE (org_id, path);

-- Sane segments and a bounded depth (8 levels) — checked at the door, not by policy.
ALTER TABLE teams ADD CONSTRAINT teams_slug_shape CHECK (slug ~ '^[a-z0-9][a-z0-9-]*$');
ALTER TABLE teams ADD CONSTRAINT teams_path_depth CHECK (array_length(string_to_array(path, '/'), 1) <= 8);

CREATE INDEX idx_teams_parent ON teams(parent_team_id);
-- Subtree scans: WHERE org_id = $1 AND (path = $2 OR path LIKE $2 || '/%')
CREATE INDEX idx_teams_org_path ON teams(org_id, path text_pattern_ops);
