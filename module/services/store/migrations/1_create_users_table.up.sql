-- Enable UUID generation
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

-- User status enum
CREATE TYPE user_status AS ENUM ('active', 'inactive', 'suspended', 'deleted');

-- Main users table
CREATE TABLE IF NOT EXISTS "users" (
uuid            UUID DEFAULT gen_random_uuid() PRIMARY KEY,
primary_email   TEXT NOT NULL,
created_at      TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
updated_at      TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
last_login      TIMESTAMP WITH TIME ZONE,
status          user_status NOT NULL DEFAULT 'active',
profile         JSONB DEFAULT '{}'::jsonb,
email_verified  BOOLEAN DEFAULT false,
CONSTRAINT users_primary_email_check CHECK (primary_email ~* '^[A-Za-z0-9._+%-]+@[A-Za-z0-9.-]+[.][A-Za-z]+$')
);

-- Email must be unique (case insensitive)
CREATE UNIQUE INDEX idx_users_primary_email_lower ON users (LOWER(primary_email));

-- Index for status queries
CREATE INDEX idx_users_status ON users (status);

-- JSONB index for common profile queries
CREATE INDEX idx_users_profile ON users USING gin (profile jsonb_path_ops);


-- Identity providers table (optional, but recommended for validation)
CREATE TABLE IF NOT EXISTS "identity_providers" (
provider_id     TEXT PRIMARY KEY,
name            TEXT NOT NULL,
enabled         BOOLEAN DEFAULT true,
created_at      TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
config          JSONB DEFAULT '{}'::jsonb
);

-- Insert common providers
INSERT INTO identity_providers (provider_id, name) VALUES
('google', 'Google'),
('facebook', 'Facebook'),
('apple', 'Apple'),
('email', 'Email/Password'),
('github', 'GitHub'),
('workos', 'WorkOS')
ON CONFLICT (provider_id) DO NOTHING;

-- User identities table

CREATE TABLE IF NOT EXISTS "user_identities" (
 uuid            UUID DEFAULT gen_random_uuid() PRIMARY KEY,
 user_uuid       UUID NOT NULL REFERENCES users(uuid) ON DELETE CASCADE,
 provider        TEXT NOT NULL REFERENCES identity_providers(provider_id),
 provider_id     TEXT NOT NULL,
 provider_email  TEXT NOT NULL,
 created_at      TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
 updated_at      TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
 last_used       TIMESTAMP WITH TIME ZONE,
 provider_data   JSONB DEFAULT '{}'::jsonb,
 email_verified  BOOLEAN DEFAULT false,

-- Each provider ID should be unique
CONSTRAINT user_identities_provider_unique UNIQUE (provider, provider_id),
-- Email format validation
CONSTRAINT user_identities_email_check CHECK (provider_email ~* '^[A-Za-z0-9._+%-]+@[A-Za-z0-9.-]+[.][A-Za-z]+$')
);

-- Indexes for user_identities
CREATE INDEX idx_user_identities_user_uuid ON user_identities (user_uuid);
CREATE INDEX idx_user_identities_provider ON user_identities (provider);
CREATE INDEX idx_user_identities_provider_lookup ON user_identities (provider, provider_id);
CREATE INDEX idx_user_identities_email_lower ON user_identities (LOWER(provider_email));
CREATE INDEX idx_user_identities_provider_data ON user_identities USING gin (provider_data jsonb_path_ops);

-- -- Email change history (audit trail)
-- CREATE TABLE IF NOT EXISTS "email_change_history" (
--                                                       uuid            UUID DEFAULT gen_random_uuid() PRIMARY KEY,
--                                                       user_uuid       UUID NOT NULL REFERENCES users(uuid) ON DELETE CASCADE,
--                                                       old_email       TEXT NOT NULL,
--                                                       new_email       TEXT NOT NULL,
--                                                       changed_at      TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
--                                                       reason          TEXT,
--                                                       changed_by      UUID REFERENCES users(uuid)
-- );
--
-- CREATE INDEX idx_email_change_history_user ON email_change_history (user_uuid);
-- CREATE INDEX idx_email_change_history_date ON email_change_history (changed_at);
--
-- -- Function to update timestamps
-- CREATE OR REPLACE FUNCTION update_updated_at_column()
--     RETURNS TRIGGER AS $$
-- BEGIN
--     NEW.updated_at = CURRENT_TIMESTAMP;
--     RETURN NEW;
-- END;
-- $$ language 'plpgsql';
--
-- -- Trigger for users table
-- CREATE TRIGGER update_users_timestamp
--     BEFORE UPDATE ON users
--     FOR EACH ROW
-- EXECUTE FUNCTION update_updated_at_column();
--
-- -- Trigger for user_identities table
-- CREATE TRIGGER update_user_identities_timestamp
--     BEFORE UPDATE ON user_identities
--     FOR EACH ROW
-- EXECUTE FUNCTION update_updated_at_column();
--
-- -- Function to log email changes
-- CREATE OR REPLACE FUNCTION log_email_change()
--     RETURNS TRIGGER AS $$
-- BEGIN
--     IF OLD.primary_email != NEW.primary_email THEN
--         INSERT INTO email_change_history (user_uuid, old_email, new_email)
--         VALUES (NEW.uuid, OLD.primary_email, NEW.primary_email);
--     END IF;
--     RETURN NEW;
-- END;
-- $$ language 'plpgsql';
--
-- -- Trigger for email change logging
-- CREATE TRIGGER log_email_changes
--     AFTER UPDATE OF primary_email ON users
--     FOR EACH ROW
-- EXECUTE FUNCTION log_email_change();
--
-- -- Useful view for user management
-- CREATE OR REPLACE VIEW user_identities_view AS
-- SELECT
--     u.uuid as user_uuid,
--     u.primary_email,
--     u.status,
--     u.email_verified as primary_email_verified,
--     u.last_login,
--     ui.provider,
--     ui.provider_id,
--     ui.provider_email,
--     ui.email_verified as provider_email_verified,
--     ui.last_used as identity_last_used,
--     ip.name as provider_name
-- FROM users u
--          LEFT JOIN user_identities ui ON u.uuid = ui.user_uuid
--          LEFT JOIN identity_providers ip ON ui.provider = ip.provider_id;
--
-- -- Common queries as functions
-- CREATE OR REPLACE FUNCTION find_user_by_provider(
--     p_provider TEXT,
--     p_provider_id TEXT
-- ) RETURNS TABLE (
--                     user_uuid UUID,
--                     primary_email TEXT,
--                     status user_status
--                 ) AS $$
-- BEGIN
--     RETURN QUERY
--         SELECT u.uuid, u.primary_email, u.status
--         FROM users u
--                  JOIN user_identities ui ON u.uuid = ui.user_uuid
--         WHERE ui.provider = p_provider
--           AND ui.provider_id = p_provider_id;
-- END;
-- $$ LANGUAGE plpgsql;
--
--
-- CREATE TABLE IF NOT EXISTS "permissions"
-- (
--     id       UUID PRIMARY KEY,
--     name     TEXT NOT NULL,
--     resource TEXT NOT NULL,
--     access   TEXT NOT NULL
-- );
--
-- CREATE INDEX idx_permissions_id ON permissions (id);
--
-- CREATE TABLE IF NOT EXISTS "roles"
-- (
--     id   UUID PRIMARY KEY,
--     name TEXT NOT NULL
-- );
--
-- CREATE INDEX idx_roles_id ON roles (id);
--
--
-- CREATE TABLE IF NOT EXISTS "role_permissions"
-- (
--     role_id       UUID NOT NULL,
--     permission_id UUID NOT NULL,
--     PRIMARY KEY (role_id, permission_id),
--     FOREIGN KEY (role_id) REFERENCES roles (id) ON DELETE CASCADE,
--     FOREIGN KEY (permission_id) REFERENCES permissions (id) ON DELETE CASCADE
-- );
--
-- CREATE INDEX idx_role_permissions_role_id ON role_permissions (role_id);
-- CREATE INDEX idx_role_permissions_permission_id ON role_permissions (permission_id);
