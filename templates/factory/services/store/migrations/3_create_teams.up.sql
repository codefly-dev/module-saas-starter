-- Teams: groups of users within an organization
CREATE TABLE IF NOT EXISTS "teams" (
    id          UUID DEFAULT gen_random_uuid() PRIMARY KEY,
    org_id      UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    description TEXT,
    created_at  TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(org_id, name)
);

CREATE INDEX idx_teams_org_id ON teams(org_id);

CREATE TABLE IF NOT EXISTS "team_members" (
    team_id     UUID NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    user_id     UUID NOT NULL REFERENCES users(uuid) ON DELETE CASCADE,
    role        TEXT NOT NULL DEFAULT 'member' CHECK (role IN ('member', 'admin', 'owner')),
    joined_at   TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (team_id, user_id)
);

CREATE INDEX idx_team_members_user_id ON team_members(user_id);
