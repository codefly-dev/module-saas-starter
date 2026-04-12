CREATE TABLE IF NOT EXISTS "organizations"
(
    id     UUID PRIMARY KEY,
    name   TEXT NOT NULL,
    domain TEXT

);

CREATE INDEX idx_organizations_id ON organizations (id);

CREATE TABLE IF NOT EXISTS "organization_users"
(
    organization_id UUID NOT NULL,
    user_id         UUID NOT NULL,
    role            TEXT NOT NULL,
    joined_at       TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (organization_id, user_id),
    FOREIGN KEY (organization_id) REFERENCES organizations (id),
    FOREIGN KEY (user_id) REFERENCES users (id)
);

CREATE INDEX idx_user_organizations_user_id ON organization_users (user_id);
CREATE INDEX idx_user_organizations_organization_id ON organization_users (organization_id);

CREATE TABLE IF NOT EXISTS organization_roles
(
    id              UUID PRIMARY KEY,
    organization_id UUID NOT NULL,
    name            TEXT NOT NULL,
    FOREIGN KEY (organization_id) REFERENCES organizations (id)
);

CREATE INDEX idx_organization_roles_id ON organization_roles (id);
CREATE INDEX idx_organization_roles_organization_id ON organization_roles (organization_id);


CREATE TABLE IF NOT EXISTS user_organization_roles
(
    user_id         UUID NOT NULL,
    organization_id UUID NOT NULL,
    role_id         UUID NOT NULL,
    PRIMARY KEY (user_id, organization_id, role_id),
    FOREIGN KEY (user_id) REFERENCES users (id),
    FOREIGN KEY (organization_id) REFERENCES organizations (id),
    FOREIGN KEY (role_id) REFERENCES organization_roles (id)
);

CREATE INDEX idx_user_organization_roles_user_id ON user_organization_roles (user_id);
CREATE INDEX idx_user_organization_roles_organization_id ON user_organization_roles (organization_id);
CREATE INDEX idx_user_organization_roles_role_id ON user_organization_roles (role_id);
