-- Linked devices — generic external-device ↔ organization pairing.
--
-- linked_devices      — which org owns which device identity (public key).
-- device_claim_codes  — short-lived, hashed claim codes minted by an org
--                       admin and redeemed by the device (mirrors the
--                       invitation token pattern: only a SHA-256 hash is
--                       stored, rows expire).
--
-- Products use this to pair an external device/agent (e.g. a lazybox box, a
-- CLI agent host) with an organization. The service-to-service entitlement
-- check resolves device_public_key → org → subscription →
-- plan_entitlements(<entitlement_key>); claim admission enforces the
-- 'paired_devices' entitlement inside the same tenant transaction.

CREATE TABLE linked_devices (
    id                UUID PRIMARY KEY,
    org_id            UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    -- Public key (e.g. base64 Ed25519) presented by the device. Globally
    -- unique: one device belongs to exactly one org until revoked-and-reclaimed.
    device_public_key TEXT NOT NULL UNIQUE
        CHECK (length(device_public_key) BETWEEN 1 AND 256),
    name              TEXT NOT NULL DEFAULT '' CHECK (length(name) <= 200),
    created_by        UUID REFERENCES users(uuid) ON DELETE SET NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    revoked_at        TIMESTAMPTZ
);

CREATE INDEX linked_devices_org_idx ON linked_devices(org_id);

CREATE TABLE device_claim_codes (
    id                UUID PRIMARY KEY,
    org_id            UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    -- SHA-256 hex of the plaintext claim code; the plaintext is returned
    -- once at mint time and never stored (invitation token recipe).
    code_hash         TEXT NOT NULL UNIQUE,
    created_by        UUID REFERENCES users(uuid) ON DELETE SET NULL,
    status            TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'used', 'expired', 'revoked')),
    expires_at        TIMESTAMPTZ NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    used_at           TIMESTAMPTZ,
    used_by_device_id UUID REFERENCES linked_devices(id) ON DELETE SET NULL
);

CREATE INDEX device_claim_codes_org_idx ON device_claim_codes(org_id);

-- RLS — direct org_id policy, fail-closed. No application-settable bypass
-- branch (migration 68 removed the GUC bypass): cross-tenant lookups (claim
-- redemption by code hash, entitlement check by device public key) go
-- through the audited BYPASSRLS app_control_plane role instead.
ALTER TABLE linked_devices ENABLE ROW LEVEL SECURITY;
ALTER TABLE linked_devices FORCE  ROW LEVEL SECURITY;
CREATE POLICY linked_devices_tenant ON linked_devices
    USING (org_id::text = current_setting('app.current_org_id', true))
    WITH CHECK (org_id::text = current_setting('app.current_org_id', true));

ALTER TABLE device_claim_codes ENABLE ROW LEVEL SECURITY;
ALTER TABLE device_claim_codes FORCE  ROW LEVEL SECURITY;
CREATE POLICY device_claim_codes_tenant ON device_claim_codes
    USING (org_id::text = current_setting('app.current_org_id', true))
    WITH CHECK (org_id::text = current_setting('app.current_org_id', true));

-- Deliberate grants (migration 61 baseline: new relations start inaccessible).
REVOKE ALL PRIVILEGES ON linked_devices
FROM PUBLIC, app_tenant, app_control_plane, app_billing_worker, app_webhook_worker, app_job_worker;
REVOKE ALL PRIVILEGES ON device_claim_codes
FROM PUBLIC, app_tenant, app_control_plane, app_billing_worker, app_webhook_worker, app_job_worker;

-- Tenant paths: mint/list/revoke devices and codes inside WithOrgTx.
GRANT SELECT, INSERT, UPDATE ON linked_devices TO app_tenant;
GRANT SELECT, INSERT, UPDATE ON device_claim_codes TO app_tenant;

-- Control-plane authority is exact-DML on every tenant relation (see
-- TestControlPlaneRelationGrantsAreExact): cross-tenant lookups (code_hash →
-- org, device_public_key → org) plus audited maintenance/cleanup.
GRANT SELECT, INSERT, UPDATE, DELETE ON linked_devices TO app_control_plane;
GRANT SELECT, INSERT, UPDATE, DELETE ON device_claim_codes TO app_control_plane;
