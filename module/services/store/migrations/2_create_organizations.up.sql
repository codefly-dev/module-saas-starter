-- Organizations: every user belongs to at least one
CREATE TABLE IF NOT EXISTS "organizations" (
    id          UUID DEFAULT gen_random_uuid() PRIMARY KEY,
    name        TEXT NOT NULL,
    slug        TEXT NOT NULL,
    owner_id    UUID NOT NULL REFERENCES users(uuid) ON DELETE RESTRICT,
    created_at  TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    updated_at  TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX idx_organizations_slug ON organizations(LOWER(slug));

-- Link users to organizations (a user can belong to multiple orgs)
CREATE TABLE IF NOT EXISTS "organization_members" (
    org_id      UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    user_id     UUID NOT NULL REFERENCES users(uuid) ON DELETE CASCADE,
    role        TEXT NOT NULL DEFAULT 'member' CHECK (role IN ('member', 'admin', 'owner')),
    joined_at   TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (org_id, user_id)
);

CREATE INDEX idx_org_members_user_id ON organization_members(user_id);
