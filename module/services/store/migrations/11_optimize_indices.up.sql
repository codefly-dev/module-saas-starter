-- =============================================================================
-- Migration 11: Index optimizations and performance improvements
-- =============================================================================

-- ---------------------------------------------------------------------------
-- 1. REMOVE REDUNDANT INDICES
--    UNIQUE constraints already create implicit B-tree indices. These
--    duplicates waste disk space and slow down every INSERT/UPDATE.
-- ---------------------------------------------------------------------------

-- api_keys.key_hash already has a UNIQUE constraint (migration 5)
DROP INDEX IF EXISTS idx_api_keys_hash;

-- sessions.refresh_token_hash already has a UNIQUE constraint (migration 6)
DROP INDEX IF EXISTS idx_sessions_refresh_token_hash;

-- invitations.token_hash already has a UNIQUE constraint (migration 8)
DROP INDEX IF EXISTS idx_invitations_token;

-- feature_flags.name already has a UNIQUE constraint (migration 10)
DROP INDEX IF EXISTS idx_feature_flags_name;

-- ---------------------------------------------------------------------------
-- 2. MISSING FOREIGN KEY INDEX
--    organizations.owner_id references users(uuid) with ON DELETE RESTRICT.
--    Without an index, every user deletion check sequentially scans
--    organizations. Also needed for "list orgs owned by user" queries.
-- ---------------------------------------------------------------------------

CREATE INDEX idx_organizations_owner_id ON organizations(owner_id);

-- ---------------------------------------------------------------------------
-- 3. PARTIAL INDICES FOR COMMON FILTERED QUERIES
--    These are smaller than full indices and match the exact WHERE clauses
--    used by the application, giving the planner a direct path.
-- ---------------------------------------------------------------------------

-- ListAPIKeys(org_id) WHERE revoked_at IS NULL
-- Only active (non-revoked) keys matter for listing and authorization.
CREATE INDEX idx_api_keys_org_active
    ON api_keys(organization_id)
    WHERE revoked_at IS NULL;

-- ListActiveSessions(user_id) WHERE revoked_at IS NULL
-- Active session lookups should not scan revoked/expired rows.
CREATE INDEX idx_sessions_user_active
    ON sessions(user_id)
    WHERE revoked_at IS NULL;

-- CountPendingInvitations(org_id) WHERE status = 'pending'
-- Entitlement checks frequently count pending invites per org.
CREATE INDEX idx_invitations_org_pending
    ON invitations(org_id)
    WHERE status = 'pending';

-- ---------------------------------------------------------------------------
-- 4. COMPOSITE INDEX FOR ORGANIZATION MEMBERS
--    ListOrgMembers(org_id) is covered by the PK (org_id, user_id), but
--    the PK already starts with org_id so this is handled. Add a covering
--    index that includes role for index-only scans on member listings.
-- ---------------------------------------------------------------------------

CREATE INDEX idx_org_members_org_role
    ON organization_members(org_id, role);

-- ---------------------------------------------------------------------------
-- 5. ROLE ASSIGNMENT LOOKUPS
--    Checking permissions requires looking up role_id for a given subject
--    within an org. The existing idx_role_assignments_subject covers
--    (subject_id, subject_kind) but not org_id. Add a composite index
--    for the full permission resolution path.
-- ---------------------------------------------------------------------------

CREATE INDEX idx_role_assignments_subject_org
    ON role_assignments(subject_id, subject_kind, org_id);

-- ---------------------------------------------------------------------------
-- 6. JSONB / ARRAY GIN INDICES FOR QUERIED DOCUMENT COLUMNS
-- ---------------------------------------------------------------------------

-- audit_events.metadata: compliance queries filter on metadata contents
CREATE INDEX idx_audit_events_metadata
    ON audit_events USING gin (metadata jsonb_path_ops);

-- api_keys.scopes: authorization checks use containment queries on scopes
CREATE INDEX idx_api_keys_scopes
    ON api_keys USING gin (scopes jsonb_path_ops);

-- feature_flags.target_org_ids: "is this org targeted?" uses array containment
CREATE INDEX idx_feature_flags_target_orgs
    ON feature_flags USING gin (target_org_ids);

-- ---------------------------------------------------------------------------
-- 7. SUBSCRIPTIONS: index on plan_id for plan-level queries
--    e.g., "how many orgs are on the free plan?" or joining to plan_entitlements
-- ---------------------------------------------------------------------------

CREATE INDEX idx_subscriptions_plan_id ON subscriptions(plan_id);

-- ---------------------------------------------------------------------------
-- 8. ENTITLEMENT OVERRIDES: index for non-expired overrides lookup
--    When resolving effective entitlements, expired overrides are skipped.
-- ---------------------------------------------------------------------------

-- Entitlement overrides lookup by org + feature (expiry checked at query time)
CREATE INDEX idx_entitlement_overrides_org_feature
    ON entitlement_overrides(org_id, feature);

-- ---------------------------------------------------------------------------
-- 9. SESSIONS: index for cleanup of expired sessions
--    A background job needs to find expired sessions efficiently.
-- ---------------------------------------------------------------------------

CREATE INDEX idx_sessions_expires_at
    ON sessions(expires_at)
    WHERE revoked_at IS NULL;

-- ---------------------------------------------------------------------------
-- 10. AUDIT_EVENTS: prepare for future partitioning
--     Adding a BRIN index on created_at for time-range scans. BRIN indices
--     are tiny (a few KB) and extremely effective for append-only tables
--     where physical row order correlates with the indexed column.
--
--     NOTE: For production at scale, consider converting audit_events to a
--     partitioned table (PARTITION BY RANGE (created_at)) with monthly
--     partitions. This migration adds the BRIN index as a non-breaking
--     improvement; partitioning requires a table rebuild and should be
--     done as a separate, coordinated migration.
-- ---------------------------------------------------------------------------

CREATE INDEX idx_audit_events_created_at_brin
    ON audit_events USING brin (created_at)
    WITH (pages_per_range = 32);

-- ---------------------------------------------------------------------------
-- 11. USERS: index on created_at for admin listing/pagination
-- ---------------------------------------------------------------------------

CREATE INDEX idx_users_created_at ON users(created_at DESC);

-- ---------------------------------------------------------------------------
-- 12. USAGE_RECORDS: index for period-based cleanup
--     Allows efficient deletion of old periods without full table scan.
-- ---------------------------------------------------------------------------

CREATE INDEX idx_usage_records_period ON usage_records(period);
