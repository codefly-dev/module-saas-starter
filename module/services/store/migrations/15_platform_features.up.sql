-- Migration 15: Outbound webhooks, notifications, onboarding, GDPR, MFA
-- Adds tables for the remaining SaaS platform features.

-- ════════════════════════════════════════════════════════════════
-- Outbound Webhooks
-- ════════════════════════════════════════════════════════════════

CREATE TABLE webhook_subscriptions (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id      UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    url         TEXT NOT NULL,
    secret      TEXT NOT NULL,  -- HMAC-SHA256 signing secret
    events      TEXT[] NOT NULL DEFAULT '{}',  -- event types to subscribe to
    active      BOOLEAN NOT NULL DEFAULT true,
    description TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_webhook_subs_org ON webhook_subscriptions(org_id) WHERE active = true;

CREATE TABLE webhook_deliveries (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    subscription_id UUID NOT NULL REFERENCES webhook_subscriptions(id) ON DELETE CASCADE,
    event_type      TEXT NOT NULL,
    payload         TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'delivered', 'failed')),
    http_status     INT,
    response_body   TEXT,
    attempts        INT NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    delivered_at    TIMESTAMPTZ
);

CREATE INDEX idx_webhook_deliveries_sub ON webhook_deliveries(subscription_id, created_at DESC);

-- ════════════════════════════════════════════════════════════════
-- In-App Notifications
-- ════════════════════════════════════════════════════════════════

CREATE TABLE notifications (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID NOT NULL REFERENCES users(uuid) ON DELETE CASCADE,
    org_id      UUID REFERENCES organizations(id) ON DELETE CASCADE,
    title       TEXT NOT NULL,
    body        TEXT NOT NULL DEFAULT '',
    type        TEXT NOT NULL DEFAULT 'info' CHECK (type IN ('info', 'success', 'warning', 'error', 'billing', 'security')),
    action_url  TEXT,
    read_at     TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_notifications_user_unread ON notifications(user_id, created_at DESC) WHERE read_at IS NULL;
CREATE INDEX idx_notifications_user ON notifications(user_id, created_at DESC);

-- ════════════════════════════════════════════════════════════════
-- Onboarding Progress
-- ════════════════════════════════════════════════════════════════

CREATE TABLE onboarding_progress (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      UUID NOT NULL REFERENCES users(uuid) ON DELETE CASCADE,
    step_name    TEXT NOT NULL CHECK (step_name IN ('create_org', 'invite_team', 'choose_plan', 'setup_api_key')),
    status       TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'completed', 'skipped')),
    completed_at TIMESTAMPTZ,
    UNIQUE(user_id, step_name)
);

-- ════════════════════════════════════════════════════════════════
-- GDPR Requests
-- ════════════════════════════════════════════════════════════════

CREATE TABLE gdpr_requests (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      UUID NOT NULL REFERENCES users(uuid),
    type         TEXT NOT NULL CHECK (type IN ('export', 'deletion')),
    status       TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'processing', 'completed', 'failed')),
    download_url TEXT,
    expires_at   TIMESTAMPTZ,
    error        TEXT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at TIMESTAMPTZ
);

CREATE INDEX idx_gdpr_requests_user ON gdpr_requests(user_id, created_at DESC);

-- ════════════════════════════════════════════════════════════════
-- MFA (Multi-Factor Authentication)
-- ════════════════════════════════════════════════════════════════

CREATE TABLE mfa_devices (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id          UUID NOT NULL REFERENCES users(uuid) ON DELETE CASCADE,
    device_type      TEXT NOT NULL DEFAULT 'totp' CHECK (device_type IN ('totp', 'webauthn')),
    name             TEXT NOT NULL DEFAULT 'Authenticator',
    secret_encrypted TEXT NOT NULL,  -- TOTP secret, encrypted at rest
    verified_at      TIMESTAMPTZ,
    last_used_at     TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_mfa_devices_user ON mfa_devices(user_id);

CREATE TABLE mfa_backup_codes (
    id        UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id   UUID NOT NULL REFERENCES users(uuid) ON DELETE CASCADE,
    code_hash TEXT NOT NULL,  -- bcrypt hash of the backup code
    used_at   TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_mfa_backup_user ON mfa_backup_codes(user_id) WHERE used_at IS NULL;

-- ════════════════════════════════════════════════════════════════
-- Email Templates (system-managed)
-- ════════════════════════════════════════════════════════════════

CREATE TABLE email_templates (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name             TEXT NOT NULL UNIQUE,
    subject_template TEXT NOT NULL,
    html_template    TEXT NOT NULL,
    text_template    TEXT NOT NULL DEFAULT '',
    version          INT NOT NULL DEFAULT 1,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Seed built-in templates
INSERT INTO email_templates (name, subject_template, html_template, text_template) VALUES
('welcome', 'Welcome to {{app_name}}', '<h1>Welcome, {{user_name}}!</h1><p>Your account has been created. <a href="{{app_url}}">Get started</a>.</p>', 'Welcome, {{user_name}}! Your account has been created. Get started: {{app_url}}'),
('invitation', 'You''ve been invited to {{org_name}}', '<h1>Join {{org_name}}</h1><p>{{inviter_name}} has invited you to join {{org_name}} as {{role}}.</p><p><a href="{{invite_url}}">Accept invitation</a></p>', '{{inviter_name}} has invited you to join {{org_name}} as {{role}}. Accept: {{invite_url}}'),
('magic_link', 'Your sign-in link', '<h1>Sign in to your account</h1><p><a href="{{link_url}}">Sign in</a>. This link expires in 15 minutes.</p>', 'Sign in to your account (expires in 15 minutes): {{link_url}}'),
('payment_failed', 'Subscription payment failed', '<h1>Payment Failed</h1><p>We couldn''t process your subscription payment. <a href="{{billing_url}}">Manage billing</a> to update your payment method.</p>', 'We couldn''t process your subscription payment. Manage billing: {{billing_url}}'),
('invoice_ready', 'Subscription payment received', '<h1>Payment Received</h1><p>Your subscription payment was received. <a href="{{billing_url}}">View billing</a>.</p>', 'Your subscription payment was received. View billing: {{billing_url}}'),
('trial_ending', 'Your subscription trial is ending soon', '<h1>Trial Ending Soon</h1><p>Your subscription trial is ending soon. <a href="{{billing_url}}">Manage your subscription</a> to keep your account.</p>', 'Your subscription trial is ending soon. Manage your subscription: {{billing_url}}'),
('gdpr_export_ready', 'Your data export is ready', '<h1>Data Export Ready</h1><p>Your data export for {{user_email}} is ready. <a href="{{download_url}}">Download</a> (expires {{expires_at}}).</p>', 'Your data export is ready. Download: {{download_url}} (expires {{expires_at}})');
