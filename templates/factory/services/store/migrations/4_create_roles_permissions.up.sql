-- Roles: named sets of permissions
-- org_id NULL = global built-in role, org_id set = custom role for that org
CREATE TABLE IF NOT EXISTS "roles" (
    id          UUID DEFAULT gen_random_uuid() PRIMARY KEY,
    name        TEXT NOT NULL,
    description TEXT,
    built_in    BOOLEAN NOT NULL DEFAULT false,
    org_id      UUID REFERENCES organizations(id) ON DELETE CASCADE,
    created_at  TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(org_id, name)
);

-- Permissions: what a role can do (resource:action pairs)
CREATE TABLE IF NOT EXISTS "role_permissions" (
    role_id     UUID NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    resource    TEXT NOT NULL,   -- e.g. "users", "billing", "knowledge", "*"
    action      TEXT NOT NULL,   -- e.g. "read", "write", "admin", "*"
    PRIMARY KEY (role_id, resource, action)
);

-- Role assignments: link a user or team to a role, optionally scoped
CREATE TABLE IF NOT EXISTS "role_assignments" (
    id              UUID DEFAULT gen_random_uuid() PRIMARY KEY,
    subject_id      UUID NOT NULL,
    subject_kind    TEXT NOT NULL CHECK (subject_kind IN ('user', 'team')),
    role_id         UUID NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    org_id          UUID REFERENCES organizations(id) ON DELETE CASCADE,
    scope           TEXT,           -- optional fine-grained scope e.g. "projects/foo"
    assigned_at     TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(subject_id, role_id, org_id, scope)
);

CREATE INDEX idx_role_assignments_subject ON role_assignments(subject_id, subject_kind);
CREATE INDEX idx_role_assignments_org ON role_assignments(org_id);

-- Seed built-in roles
INSERT INTO roles (name, description, built_in, org_id) VALUES
    ('admin',  'Full access to all resources',                  true, NULL),
    ('editor', 'Read and write access to domain resources',     true, NULL),
    ('viewer', 'Read-only access',                              true, NULL)
ON CONFLICT DO NOTHING;

-- Admin: wildcard
INSERT INTO role_permissions (role_id, resource, action)
SELECT r.id, '*', '*' FROM roles r WHERE r.name = 'admin' AND r.built_in = true
ON CONFLICT DO NOTHING;

-- Editor: broad read/write
INSERT INTO role_permissions (role_id, resource, action)
SELECT r.id, p.resource, p.action FROM roles r
CROSS JOIN (VALUES
    ('users',      'read'),  ('users',      'write'),
    ('teams',      'read'),  ('teams',      'write'),
    ('knowledge',  'read'),  ('knowledge',  'write'),
    ('billing',    'read')
) AS p(resource, action)
WHERE r.name = 'editor' AND r.built_in = true
ON CONFLICT DO NOTHING;

-- Viewer: read-only
INSERT INTO role_permissions (role_id, resource, action)
SELECT r.id, p.resource, p.action FROM roles r
CROSS JOIN (VALUES
    ('users',     'read'),
    ('teams',     'read'),
    ('knowledge', 'read')
) AS p(resource, action)
WHERE r.name = 'viewer' AND r.built_in = true
ON CONFLICT DO NOTHING;
