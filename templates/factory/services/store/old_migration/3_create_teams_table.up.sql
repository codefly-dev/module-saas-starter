CREATE TABLE IF NOT EXISTS "teams"
(
    id              UUID PRIMARY KEY,
    name            TEXT NOT NULL,
    organization_id UUID NOT NULL,
    FOREIGN KEY (organization_id) REFERENCES organizations (id)
);

CREATE INDEX idx_teams_id ON teams (id);
CREATE INDEX idx_teams_organization_id ON teams (organization_id);

CREATE TABLE IF NOT EXISTS team_members
(
    team_id   UUID NOT NULL,
    user_id   UUID NOT NULL,
    role_id   UUID NOT NULL,
    joined_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (team_id, user_id),
    FOREIGN KEY (team_id) REFERENCES teams (id) ON DELETE CASCADE,
    FOREIGN KEY (user_id) REFERENCES users (id),
    FOREIGN KEY (role_id) REFERENCES roles (id)
);

CREATE INDEX idx_user_teams_team_id ON team_members (team_id);
CREATE INDEX idx_user_teams_user_id ON team_members (user_id);


CREATE TABLE IF NOT EXISTS team_roles
(
    id      UUID PRIMARY KEY,
    team_id UUID NOT NULL,
    name    TEXT NOT NULL,
    FOREIGN KEY (team_id) REFERENCES teams (id)
);

CREATE INDEX idx_team_roles_id ON team_roles (id);
CREATE INDEX idx_team_roles_team_id ON team_roles (team_id);


CREATE TABLE IF NOT EXISTS user_team_roles
(
    user_id UUID NOT NULL,
    team_id UUID NOT NULL,
    role_id UUID NOT NULL,
    PRIMARY KEY (user_id, team_id, role_id),
    FOREIGN KEY (user_id) REFERENCES users (id),
    FOREIGN KEY (team_id) REFERENCES teams (id),
    FOREIGN KEY (role_id) REFERENCES team_roles (id)
);

CREATE INDEX idx_user_team_roles_user_id ON user_team_roles (user_id);
CREATE INDEX idx_user_team_roles_team_id ON user_team_roles (team_id);
CREATE INDEX idx_user_team_roles_role_id ON user_team_roles (role_id);
