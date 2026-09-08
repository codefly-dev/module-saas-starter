-- Namespace every audit event type under the module's own namespace (issue #520).
--
-- `auth.login` becomes `saas.auth.login`. A composed workspace hosts several
-- modules against one audit spine, so a bare `<aggregate>.<event>` name is a
-- collision waiting to happen: two modules both mint `user.created` and the
-- webhook fan-out routes on a string that no longer identifies a producer. The
-- shape is the one EVENTS.md already fixed for domain events —
-- `<namespace>.<aggregate>.<event>` — so the two event systems stop disagreeing.
--
-- This is a hard cutover, pre-1.0: no alias layer, no dual read. The rewrite is
-- one-time and in place, and history is rewritten rather than dropped (unlike
-- migration 97, which recreated audit_events wholesale). Stored webhook
-- subscriptions are rewritten with it, so an existing subscriber keeps firing.
--
-- Order matters: rewrite every stored value first, then constrain, so no
-- constraint is added against a table that still holds legacy values.
--
-- Classification (DATABASE_AUTHORITY.md): no new relation and no change to any
-- tenant boundary. audit_event_types stays global control-plane reference data;
-- audit_events stays a tenant relation under its existing RLS policy and grants.

ALTER TABLE audit_event_types ADD COLUMN namespace TEXT NOT NULL DEFAULT 'saas';

-- SyncAuditEventTypes deprecates rather than deletes a type the code catalog
-- dropped, so the projection can hold legacy names no producer can emit any
-- more. Prefixing those would mint namespaced names for a dead vocabulary; drop
-- the ones no history references and namespace the rest.
DELETE FROM audit_event_types t
 WHERE t.deprecated
   AND t.name NOT LIKE 'saas.%'
   AND NOT EXISTS (SELECT 1 FROM audit_events e WHERE e.event_type = t.name);

UPDATE audit_event_types
   SET name = 'saas.' || name, updated_at = NOW()
 WHERE name NOT LIKE 'saas.%';

-- Rewrite history. audit_events is append-only by trigger (migration 97), so the
-- triggers are suspended for this transaction only — DISABLE TRIGGER needs table
-- ownership and recurses to every partition, where ADR 0003's
-- session_replication_role would have needed superuser and failed on a managed
-- Postgres.
ALTER TABLE audit_events DISABLE TRIGGER audit_events_no_update;

UPDATE audit_events
   SET event_type = 'saas.' || event_type
 WHERE event_type NOT LIKE 'saas.%';

ALTER TABLE audit_events ENABLE TRIGGER audit_events_no_update;

-- Registry validation on the internal emit path is advisory (a security event is
-- never dropped because its payload drifted), so an unregistered type can have
-- reached audit_events. Give any such value the same deprecated parent row
-- SyncAuditEventTypes would leave behind, so the foreign key below can be added
-- without deleting history.
INSERT INTO audit_event_types (name, namespace, version, category, owner, deprecated)
SELECT DISTINCT e.event_type, split_part(e.event_type, '.', 1), 1, 'system', 'unknown', TRUE
  FROM audit_events e
 WHERE NOT EXISTS (SELECT 1 FROM audit_event_types t WHERE t.name = e.event_type);

-- Existing subscribers keep firing: a subscription that named a previously
-- registered event is rewritten to the namespaced name. Anything else is left
-- alone — it never matched an event and still doesn't. (canonicalWebhookEventType
-- stays deliberately looser than the audit rule; subscription names are
-- customer-facing routing identifiers with their own compatibility story.)
UPDATE webhook_subscriptions
   SET events = ARRAY(
           SELECT CASE
                      WHEN EXISTS (SELECT 1 FROM audit_event_types t WHERE t.name = 'saas.' || e)
                          THEN 'saas.' || e
                      ELSE e
                  END
             FROM unnest(events) AS e
       ),
       updated_at = NOW()
 WHERE EXISTS (
           SELECT 1
             FROM unnest(events) AS e
             JOIN audit_event_types t ON t.name = 'saas.' || e
       );

-- Constrain, now that no legacy value survives.
ALTER TABLE audit_event_types
    ADD CONSTRAINT audit_event_types_name_format
        CHECK (name ~ '^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$'),
    ADD CONSTRAINT audit_event_types_name_in_namespace
        CHECK (name LIKE namespace || '.%');

-- At least three segments: a namespace is structurally required, so a bare
-- `<aggregate>.<event>` can no longer be written.
ALTER TABLE audit_events
    ADD CONSTRAINT audit_events_event_type_format
        CHECK (event_type ~ '^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*){2,}$');

-- The foreign key ADR 0003 specified and migration 97 omitted. Supported from a
-- partitioned table to a regular one since PG 12. SyncAuditEventTypes runs under
-- the control plane at startup before any write and deprecates rather than
-- deletes removed types, so every historical row keeps a resolvable parent. This
-- is what turns "unregistered event type" from a log line into a write error at
-- the last layer that can still catch it.
ALTER TABLE audit_events
    ADD CONSTRAINT audit_events_event_type_fkey
        FOREIGN KEY (event_type) REFERENCES audit_event_types (name);
