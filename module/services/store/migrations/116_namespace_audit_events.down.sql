-- Reverse the namespace cutover: drop the constraints, then strip the prefix
-- from every stored value in the same order the up-migration applied it.
--
-- Rollback faces the mirror image of the up-migration's hazards. An accounts
-- instance still running the pre-#520 code reconciles the registry at startup
-- and re-inserts the bare names, so the un-prefix below drops a namespaced row a
-- bare row already supersedes rather than colliding on the primary key. And both
-- rewritten tables FORCE row-level security, whose policies apply to the table
-- owner, so FORCE is suspended here exactly as it is on the way up — otherwise
-- every statement below matches zero rows and reports success.

ALTER TABLE audit_events
    DROP CONSTRAINT audit_events_event_type_fkey,
    DROP CONSTRAINT audit_events_event_type_format;

ALTER TABLE audit_event_types
    DROP CONSTRAINT audit_event_types_name_in_namespace,
    DROP CONSTRAINT audit_event_types_name_format;

ALTER TABLE audit_events NO FORCE ROW LEVEL SECURITY;
ALTER TABLE webhook_subscriptions NO FORCE ROW LEVEL SECURITY;

UPDATE webhook_subscriptions
   SET events = ARRAY(
           SELECT CASE
                      WHEN EXISTS (SELECT 1 FROM audit_event_types t WHERE t.name = e)
                          THEN substring(e FROM 6)
                      ELSE e
                  END
             FROM unnest(events) AS e
       )
 WHERE EXISTS (
           SELECT 1
             FROM unnest(events) AS e
             JOIN audit_event_types t ON t.name = e
            WHERE e LIKE 'saas.%'
       );

ALTER TABLE audit_events DISABLE TRIGGER audit_events_no_update;

UPDATE audit_events
   SET event_type = substring(event_type FROM 6)
 WHERE event_type LIKE 'saas.%';

ALTER TABLE audit_events ENABLE TRIGGER audit_events_no_update;

-- A namespaced row whose bare counterpart already exists is superseded by it
-- once the history above has been un-prefixed onto that bare name.
DELETE FROM audit_event_types t
 WHERE t.name LIKE 'saas.%'
   AND EXISTS (SELECT 1 FROM audit_event_types b WHERE b.name = substring(t.name FROM 6));

UPDATE audit_event_types
   SET name = substring(name FROM 6), updated_at = NOW()
 WHERE name LIKE 'saas.%';

ALTER TABLE audit_events FORCE ROW LEVEL SECURITY;
ALTER TABLE webhook_subscriptions FORCE ROW LEVEL SECURITY;

ALTER TABLE audit_event_types DROP COLUMN namespace;
