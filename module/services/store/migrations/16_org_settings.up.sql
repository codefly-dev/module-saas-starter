-- Migration 16: Organization branding/settings
CREATE TABLE org_settings (
    org_id        UUID PRIMARY KEY REFERENCES organizations(id) ON DELETE CASCADE,
    logo_url      TEXT NOT NULL DEFAULT '',
    primary_color TEXT NOT NULL DEFAULT '',
    custom_domain TEXT NOT NULL DEFAULT '',
    favicon_url   TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
