-- Recreate audit_events as a typed, range-partitioned Single-Table-Inheritance
-- table (issue #170, ADR 0003).
--
-- The redesign is intentionally not backward compatible: the free-form
-- action/metadata columns are replaced by a registered event_type discriminator
-- (see audit_event_types) + a typed, validated payload. Existing rows are not
-- migrated — a starter template carries no audit history worth preserving, and
-- keeping compatibility shims was explicitly out of scope.
--
-- Partitioning by created_at is what lets retention be a partition DROP rather
-- than a row DELETE: the append-only trigger rejects every row DELETE (that is
-- the whole point of immutability), so the previous DELETE-based retention was
-- silently a no-op. Dropping a whole partition is DDL and never fires the
-- row-level trigger. See DropAuditPartitionsBefore / RunRetention.

DROP TABLE IF EXISTS audit_events CASCADE;

CREATE TABLE audit_events (
    id             UUID NOT NULL,
    event_type     TEXT NOT NULL,
    schema_version INT  NOT NULL DEFAULT 1,
    actor_id       UUID,
    actor_type     TEXT NOT NULL DEFAULT 'user' CHECK (actor_type IN ('user', 'api_key', 'system', 'agent')),
    resource       TEXT NOT NULL,
    resource_id    TEXT,
    org_id         UUID,
    payload        JSONB NOT NULL DEFAULT '{}'::jsonb,
    ip_address     TEXT,
    created_at     TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP NOT NULL,
    -- A partitioned table's primary key must include the partition key.
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);

-- Indexes on the partitioned parent are cloned onto every partition, including
-- ones created later. The composites drive the per-tenant search facets; the
-- GIN index backs payload containment search.
CREATE INDEX idx_audit_events_org_type_time     ON audit_events (org_id, event_type, created_at DESC);
CREATE INDEX idx_audit_events_org_actor_time    ON audit_events (org_id, actor_id, created_at DESC);
CREATE INDEX idx_audit_events_org_resource_time ON audit_events (org_id, resource, resource_id, created_at DESC);
CREATE INDEX idx_audit_events_payload           ON audit_events USING gin (payload jsonb_path_ops);
CREATE INDEX idx_audit_events_created_at        ON audit_events (created_at DESC);

-- Append-only enforcement. Row triggers on a partitioned parent are inherited
-- by all current and future partitions (PostgreSQL 13+).
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

-- Tenant RLS. Same polymorphic shape as the pre-redesign table (migration 68):
-- tenant-scoped rows are visible/writable iff org_id matches the request
-- setting; NULL-org (system) rows are reachable only under the control plane's
-- BYPASSRLS role. The FOR ALL policy keeps the trigger-guarded UPDATE/DELETE
-- verbs admitted so the rls-migration-gate stays satisfied.
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

-- Explicit per-relation grants (required from migration 61). Audit is
-- append-only, so no role gets UPDATE/DELETE: the tenant path reads and
-- inserts; the control plane additionally writes NULL-org system events.
GRANT SELECT, INSERT ON audit_events TO app_tenant;
GRANT SELECT, INSERT ON audit_events TO app_control_plane;

-- Partition maintenance. The runtime roles deliberately hold no DDL privilege
-- (migration 67), so partition create/drop runs through SECURITY DEFINER
-- functions owned by the migration role, with EXECUTE granted only to the
-- control plane that runs startup maintenance and retention.
CREATE OR REPLACE FUNCTION audit_events_ensure_partition(month DATE)
    RETURNS void
    LANGUAGE plpgsql
    SECURITY DEFINER
    SET search_path = public
AS $$
DECLARE
    start_date DATE := date_trunc('month', month)::date;
    end_date   DATE := (date_trunc('month', month) + INTERVAL '1 month')::date;
    part_name  TEXT := 'audit_events_' || to_char(start_date, 'YYYY_MM');
BEGIN
    IF to_regclass('public.' || part_name) IS NULL THEN
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF audit_events FOR VALUES FROM (%L) TO (%L)',
            part_name, start_date, end_date
        );
    END IF;
END;
$$;

CREATE OR REPLACE FUNCTION audit_events_drop_partitions_before(cutoff TIMESTAMPTZ)
    RETURNS integer
    LANGUAGE plpgsql
    SECURITY DEFINER
    SET search_path = public
AS $$
DECLARE
    part        RECORD;
    dropped     INT := 0;
    upper_bound TIMESTAMPTZ;
BEGIN
    FOR part IN
        SELECT c.relname
        FROM pg_inherits i
        JOIN pg_class c ON c.oid = i.inhrelid
        JOIN pg_class p ON p.oid = i.inhparent
        WHERE p.relname = 'audit_events'
          AND c.relname ~ '^audit_events_[0-9]{4}_[0-9]{2}$'
    LOOP
        -- Upper bound = first instant of the month AFTER the partition's month,
        -- derived from the deterministic partition name.
        upper_bound := date_trunc(
            'month',
            to_date(substring(part.relname FROM 'audit_events_([0-9]{4}_[0-9]{2})'), 'YYYY_MM')
        ) + INTERVAL '1 month';
        IF upper_bound <= cutoff THEN
            EXECUTE format('DROP TABLE %I', part.relname);
            dropped := dropped + 1;
        END IF;
    END LOOP;
    RETURN dropped;
END;
$$;

REVOKE ALL ON FUNCTION audit_events_ensure_partition(DATE) FROM PUBLIC;
REVOKE ALL ON FUNCTION audit_events_drop_partitions_before(TIMESTAMPTZ) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION audit_events_ensure_partition(DATE) TO app_control_plane;
GRANT EXECUTE ON FUNCTION audit_events_drop_partitions_before(TIMESTAMPTZ) TO app_control_plane;

-- Seed a window of monthly partitions around now so inserts land immediately.
-- The api keeps future months provisioned at runtime (EnsureAuditPartitions).
DO $$
DECLARE
    m INT;
BEGIN
    FOR m IN -2..3 LOOP
        PERFORM audit_events_ensure_partition(
            (date_trunc('month', now()) + (m || ' month')::interval)::date
        );
    END LOOP;
END $$;
