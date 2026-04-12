CREATE TABLE IF NOT EXISTS "sessions" (
    id                 UUID DEFAULT gen_random_uuid() PRIMARY KEY,
    user_id            UUID NOT NULL REFERENCES users(uuid) ON DELETE CASCADE,
    refresh_token_hash TEXT NOT NULL UNIQUE,
    family_id          UUID NOT NULL,
    device_info        JSONB DEFAULT '{}'::jsonb,
    ip_address         TEXT,
    created_at         TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    last_active_at     TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    expires_at         TIMESTAMP WITH TIME ZONE NOT NULL,
    revoked_at         TIMESTAMP WITH TIME ZONE,
    revoked_reason     TEXT
);

CREATE INDEX idx_sessions_user_id ON sessions(user_id);
CREATE INDEX idx_sessions_family_id ON sessions(family_id);
CREATE INDEX idx_sessions_refresh_token_hash ON sessions(refresh_token_hash);
CREATE INDEX idx_sessions_active ON sessions(expires_at) WHERE revoked_at IS NULL;
