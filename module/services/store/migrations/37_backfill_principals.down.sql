-- Backfill rollback: delete principals that originated from the
-- backfill. Identifying them precisely is impossible after the fact
-- (a user-created agent could have the same shape as a backfilled
-- service); we use the join with users / api_keys as the heuristic.
--
-- Anything created via PrincipalService.CreateAgent will have a
-- principals row that has NO matching users.uuid or api_keys.id —
-- those rows are LEFT INTACT by this rollback so user-created agents
-- aren't lost on a down-migration.

DELETE FROM principals
WHERE kind = 'human'
  AND id IN (SELECT uuid FROM users);

DELETE FROM principals
WHERE kind = 'service'
  AND id IN (SELECT id FROM api_keys);
