-- Phase 2A — RLS on api_keys.
--
-- api_keys uses `organization_id` (not `org_id`) — the column-name
-- inconsistency is pre-existing. The policy just uses the right
-- column.
--
-- Cross-tenant readers that need WithBypass:
--   - ValidateAPIKey (auth flow): the plaintext key is presented
--     by an unauthenticated request; we don't yet know which tenant.
--   - GDPR export: spans every org the user belongs to.
--   - RevokeAPIKey: the proto only carries Id; no tenant on the
--     request. The handler gates on platform-admin.

ALTER TABLE api_keys ENABLE ROW LEVEL SECURITY;
ALTER TABLE api_keys FORCE  ROW LEVEL SECURITY;

CREATE POLICY api_keys_tenant ON api_keys
    USING (
        organization_id::text = current_setting('app.current_org_id', true)
        OR current_setting('app.bypass', true) = '1'
    )
    WITH CHECK (
        organization_id::text = current_setting('app.current_org_id', true)
        OR current_setting('app.bypass', true) = '1'
    );
