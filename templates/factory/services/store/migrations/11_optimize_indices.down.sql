-- Reverse migration 11: restore original index state

-- Drop all new indices
DROP INDEX IF EXISTS idx_usage_records_period;
DROP INDEX IF EXISTS idx_users_created_at;
DROP INDEX IF EXISTS idx_audit_events_created_at_brin;
DROP INDEX IF EXISTS idx_sessions_expires_at;
DROP INDEX IF EXISTS idx_entitlement_overrides_org_feature;
DROP INDEX IF EXISTS idx_subscriptions_plan_id;
DROP INDEX IF EXISTS idx_feature_flags_target_orgs;
DROP INDEX IF EXISTS idx_api_keys_scopes;
DROP INDEX IF EXISTS idx_audit_events_metadata;
DROP INDEX IF EXISTS idx_role_assignments_subject_org;
DROP INDEX IF EXISTS idx_org_members_org_role;
DROP INDEX IF EXISTS idx_invitations_org_pending;
DROP INDEX IF EXISTS idx_sessions_user_active;
DROP INDEX IF EXISTS idx_api_keys_org_active;
DROP INDEX IF EXISTS idx_organizations_owner_id;

-- Restore redundant indices that were dropped
CREATE INDEX idx_api_keys_hash ON api_keys(key_hash);
CREATE INDEX idx_sessions_refresh_token_hash ON sessions(refresh_token_hash);
CREATE INDEX idx_invitations_token ON invitations(token_hash);
CREATE INDEX idx_feature_flags_name ON feature_flags(name);
