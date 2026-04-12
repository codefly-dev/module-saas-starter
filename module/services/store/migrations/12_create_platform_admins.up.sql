-- Platform admin is orthogonal to org role.
-- A user can be platform_role=super_admin AND org_role=member simultaneously.

CREATE TYPE platform_role AS ENUM ('super_admin', 'support', 'billing');

CREATE TABLE IF NOT EXISTS "platform_admins" (
    user_id       UUID NOT NULL REFERENCES users(uuid) ON DELETE CASCADE PRIMARY KEY,
    platform_role platform_role NOT NULL,
    granted_by    UUID REFERENCES users(uuid),
    granted_at    TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_platform_admins_role ON platform_admins(platform_role);
