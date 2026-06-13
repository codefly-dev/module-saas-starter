DROP TRIGGER IF EXISTS delegation_grants_notify_decided_tr ON delegation_grants;
DROP FUNCTION IF EXISTS delegation_grants_notify_decided();
DROP INDEX IF EXISTS delegation_grants_grantor_idx;
DROP INDEX IF EXISTS delegation_grants_actor_idx;
DROP INDEX IF EXISTS delegation_grants_active_pattern_idx;
DROP INDEX IF EXISTS delegation_grants_pending_idx;
DROP TABLE IF EXISTS delegation_grants;
