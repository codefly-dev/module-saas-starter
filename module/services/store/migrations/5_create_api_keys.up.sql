CREATE TABLE IF NOT EXISTS "api_keys" (
    id              UUID DEFAULT gen_random_uuid() PRIMARY KEY,
    organization_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    user_id         UUID NOT NULL REFERENCES users(uuid) ON DELETE CASCADE,
    name            TEXT NOT NULL,
    prefix          TEXT NOT NULL,
    key_hash        TEXT NOT NULL UNIQUE,
    scopes          JSONB NOT NULL DEFAULT '[]',
    environment     TEXT NOT NULL DEFAULT 'live' CHECK (environment IN ('live', 'test')),
    expires_at      TIMESTAMP WITH TIME ZONE,
    last_used_at    TIMESTAMP WITH TIME ZONE,
    last_used_ip    TEXT,
    revoked_at      TIMESTAMP WITH TIME ZONE,
    created_by      UUID NOT NULL REFERENCES users(uuid),
    created_at      TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_api_keys_org ON api_keys(organization_id);
CREATE INDEX idx_api_keys_user ON api_keys(user_id);
CREATE INDEX idx_api_keys_hash ON api_keys(key_hash);
CREATE INDEX idx_api_keys_prefix ON api_keys(prefix);
