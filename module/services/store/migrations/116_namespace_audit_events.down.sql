-- Reverse the namespace cutover: drop the constraints, then strip the prefix
-- from every stored value in the same order the up-migration applied it.

ALTER TABLE audit_events
    DROP CONSTRAINT audit_events_event_type_fkey,
    DROP CONSTRAINT audit_events_event_type_format;

ALTER TABLE audit_event_types
    DROP CONSTRAINT audit_event_types_name_in_namespace,
    DROP CONSTRAINT audit_event_types_name_format;

UPDATE webhook_subscriptions
   SET events = ARRAY(
           SELECT CASE
                      WHEN EXISTS (SELECT 1 FROM audit_event_types t WHERE t.name = e)
                          THEN substring(e FROM 6)
                      ELSE e
                  END
             FROM unnest(events) AS e
       ),
       updated_at = NOW()
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

UPDATE audit_event_types
   SET name = substring(name FROM 6), updated_at = NOW()
 WHERE name LIKE 'saas.%';

ALTER TABLE audit_event_types DROP COLUMN namespace;
