-- Restore the pre-#170 free-form audit_events (non-partitioned).
DROP FUNCTION IF EXISTS audit_events_ensure_partition(DATE);
DROP FUNCTION IF EXISTS audit_events_drop_partitions_before(TIMESTAMPTZ);
DROP TABLE IF EXISTS audit_events CASCADE;

CREATE TABLE audit_events (
    id          UUID DEFAULT gen_random_uuid() PRIMARY KEY,
    actor_id    UUID,
    actor_type  TEXT NOT NULL DEFAULT 'user' CHECK (actor_type IN ('user', 'api_key', 'system')),
    action      TEXT NOT NULL,
    resource    TEXT NOT NULL,
    resource_id TEXT,
    org_id      UUID,
    metadata    JSONB DEFAULT '{}'::jsonb,
    ip_address  TEXT,
    created_at  TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP NOT NULL
);

CREATE OR REPLACE FUNCTION audit_events_immutable() RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'audit_events table is append-only';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER audit_events_no_update
    BEFORE UPDATE ON audit_events FOR EACH ROW
    EXECUTE FUNCTION audit_events_immutable();
CREATE TRIGGER audit_events_no_delete
    BEFORE DELETE ON audit_events FOR EACH ROW
    EXECUTE FUNCTION audit_events_immutable();

CREATE INDEX idx_audit_events_org_time ON audit_events (org_id, created_at DESC);
CREATE INDEX idx_audit_events_actor ON audit_events (actor_id);
CREATE INDEX idx_audit_events_action ON audit_events (action);
CREATE INDEX idx_audit_events_resource ON audit_events (resource, resource_id);

ALTER TABLE audit_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit_events FORCE  ROW LEVEL SECURITY;
CREATE POLICY audit_events_tenant ON audit_events
    USING (
        org_id IS NOT NULL
        AND org_id::text = current_setting('app.current_org_id', true)
    )
    WITH CHECK (
        org_id IS NOT NULL
        AND org_id::text = current_setting('app.current_org_id', true)
    );

GRANT SELECT, INSERT, UPDATE, DELETE ON audit_events TO app_tenant;
GRANT SELECT, INSERT, UPDATE, DELETE ON audit_events TO app_control_plane;
