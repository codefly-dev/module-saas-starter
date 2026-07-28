ALTER TABLE invitations
    ADD COLUMN inviter_display_name TEXT NOT NULL DEFAULT '',
    ADD COLUMN delivery_status TEXT NOT NULL DEFAULT 'disabled'
        CHECK (delivery_status IN ('disabled', 'queued', 'sent', 'delivered', 'bounced', 'complained')),
    ADD COLUMN last_sent_at TIMESTAMPTZ,
    ADD COLUMN send_count INTEGER NOT NULL DEFAULT 0 CHECK (send_count >= 0);

UPDATE invitations
SET email = LOWER(BTRIM(email)),
    last_sent_at = created_at,
    send_count = 1;

ALTER TABLE onboarding_progress
    DROP CONSTRAINT IF EXISTS onboarding_progress_user_id_step_name_key;

ALTER TABLE onboarding_progress
    ADD COLUMN org_id UUID REFERENCES organizations(id) ON DELETE CASCADE,
    ADD COLUMN flow_id TEXT NOT NULL DEFAULT 'legacy',
    ADD COLUMN flow_version INTEGER NOT NULL DEFAULT 1 CHECK (flow_version > 0),
    ADD COLUMN variant TEXT NOT NULL DEFAULT 'default',
    ADD COLUMN audience TEXT NOT NULL DEFAULT '',
    ADD COLUMN persona TEXT NOT NULL DEFAULT '',
    ADD COLUMN required BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN first_seen_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    ADD COLUMN started_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    ADD COLUMN last_seen_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    ADD COLUMN skipped_at TIMESTAMPTZ,
    ADD COLUMN completion_method TEXT NOT NULL DEFAULT 'migrated'
        CHECK (completion_method IN ('detected', 'domain_action', 'user_skip', 'migrated')),
    ADD COLUMN skip_reason TEXT NOT NULL DEFAULT '';

UPDATE onboarding_progress
SET skipped_at = completed_at
WHERE status = 'skipped';

CREATE UNIQUE INDEX onboarding_progress_legacy_unique
    ON onboarding_progress(user_id, flow_id, flow_version, step_name)
    WHERE org_id IS NULL;

CREATE UNIQUE INDEX onboarding_progress_scoped_unique
    ON onboarding_progress(user_id, org_id, flow_id, flow_version, step_name)
    WHERE org_id IS NOT NULL;

CREATE TABLE organization_activations (
    org_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    flow_id TEXT NOT NULL,
    flow_version INTEGER NOT NULL CHECK (flow_version > 0),
    milestone TEXT NOT NULL,
    achieved_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    actor_id UUID REFERENCES users(uuid) ON DELETE SET NULL,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    PRIMARY KEY (org_id, flow_id, flow_version, milestone)
);

ALTER TABLE organization_activations ENABLE ROW LEVEL SECURITY;
ALTER TABLE organization_activations FORCE ROW LEVEL SECURITY;
CREATE POLICY organization_activations_tenant ON organization_activations
    USING (org_id::text = current_setting('app.current_org_id', true))
    WITH CHECK (org_id::text = current_setting('app.current_org_id', true));

CREATE TABLE waitlist_entries (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    normalized_email TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL DEFAULT '',
    company TEXT NOT NULL DEFAULT '',
    use_case TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL DEFAULT 'pending'
        CHECK (state IN ('pending', 'verified', 'approved', 'invited', 'converted', 'rejected', 'unsubscribed')),
    verification_token_hash TEXT UNIQUE,
    verification_expires_at TIMESTAMPTZ,
    verification_sent_at TIMESTAMPTZ,
    source TEXT NOT NULL DEFAULT '',
    campaign TEXT NOT NULL DEFAULT '',
    referrer TEXT NOT NULL DEFAULT '',
    referral_code TEXT NOT NULL UNIQUE,
    referred_by UUID REFERENCES waitlist_entries(id) ON DELETE SET NULL,
    marketing_consent BOOLEAN NOT NULL DEFAULT FALSE,
    consent_policy_version TEXT NOT NULL,
    consent_recorded_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    admin_notes TEXT NOT NULL DEFAULT '',
    tags TEXT[] NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    verified_at TIMESTAMPTZ,
    approved_at TIMESTAMPTZ,
    invited_at TIMESTAMPTZ,
    converted_at TIMESTAMPTZ,
    unsubscribed_at TIMESTAMPTZ,
    converted_user_id UUID REFERENCES users(uuid) ON DELETE SET NULL,
    converted_org_id UUID REFERENCES organizations(id) ON DELETE SET NULL
);

CREATE INDEX waitlist_entries_state_created
    ON waitlist_entries(state, created_at DESC);
CREATE INDEX waitlist_entries_attribution
    ON waitlist_entries(source, campaign, created_at DESC);

ALTER TABLE waitlist_entries ENABLE ROW LEVEL SECURITY;
ALTER TABLE waitlist_entries FORCE ROW LEVEL SECURITY;
CREATE POLICY waitlist_entries_control_plane ON waitlist_entries
    USING (false)
    WITH CHECK (false);

CREATE TABLE user_consent_preferences (
    user_id UUID NOT NULL REFERENCES users(uuid) ON DELETE CASCADE,
    purpose TEXT NOT NULL CHECK (purpose IN ('necessary', 'analytics', 'marketing')),
    granted BOOLEAN NOT NULL,
    policy_version TEXT NOT NULL,
    region TEXT NOT NULL DEFAULT '',
    context TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    withdrawn_at TIMESTAMPTZ,
    PRIMARY KEY (user_id, purpose)
);

CREATE TABLE user_consent_events (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(uuid) ON DELETE CASCADE,
    purpose TEXT NOT NULL CHECK (purpose IN ('necessary', 'analytics', 'marketing')),
    granted BOOLEAN NOT NULL,
    policy_version TEXT NOT NULL,
    region TEXT NOT NULL DEFAULT '',
    context TEXT NOT NULL DEFAULT '',
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX user_consent_events_user_recorded
    ON user_consent_events(user_id, recorded_at DESC);

ALTER TABLE user_consent_preferences ENABLE ROW LEVEL SECURITY;
ALTER TABLE user_consent_preferences FORCE ROW LEVEL SECURITY;
CREATE POLICY user_consent_preferences_user ON user_consent_preferences
    USING (user_id::text = current_setting('app.current_user_id', true))
    WITH CHECK (user_id::text = current_setting('app.current_user_id', true));

ALTER TABLE user_consent_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE user_consent_events FORCE ROW LEVEL SECURITY;
CREATE POLICY user_consent_events_user ON user_consent_events
    USING (user_id::text = current_setting('app.current_user_id', true))
    WITH CHECK (user_id::text = current_setting('app.current_user_id', true));

REVOKE ALL PRIVILEGES ON
    organization_activations,
    waitlist_entries,
    user_consent_preferences,
    user_consent_events
FROM app_tenant, app_control_plane;

GRANT SELECT, INSERT, UPDATE ON
    organization_activations,
    user_consent_preferences
TO app_tenant;

GRANT SELECT, INSERT ON user_consent_events TO app_tenant;

GRANT SELECT, INSERT, UPDATE, DELETE ON
    organization_activations,
    waitlist_entries,
    user_consent_preferences,
    user_consent_events
TO app_control_plane;
