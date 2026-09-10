-- Persist the two identities an audited action has.
--
-- actor_id has always been the identity the action ran as. Under impersonation
-- that is the effective subject — the user a support admin is acting as — and
-- the person actually accountable for the action was recorded nowhere: the
-- emitter read x-is-impersonated / x-impersonated-by headers that nothing on the
-- request path ever set, and the audit_events table had no column to put them in
-- even if it had. An impersonated action was therefore indistinguishable from
-- the target performing it themselves.
--
-- impersonated_by is the real actor. It is NULL for every ordinary request,
-- where the actor and the effective subject are the same user, and is_impersonated
-- is the queryable discriminator for "these two differ".
ALTER TABLE audit_events
    ADD COLUMN IF NOT EXISTS impersonated_by UUID,
    ADD COLUMN IF NOT EXISTS is_impersonated BOOLEAN NOT NULL DEFAULT false;

-- An impersonated row must name who did it, and a row naming an impersonator
-- must be marked as one. Historical rows are all (false, NULL) and satisfy it.
ALTER TABLE audit_events
    ADD CONSTRAINT audit_events_impersonation_identity_complete
    CHECK ((is_impersonated AND impersonated_by IS NOT NULL) OR (NOT is_impersonated AND impersonated_by IS NULL));

-- Compliance review asks "what did this admin do while impersonating", which is
-- an actor-centric scan across tenants rather than a tenant-scoped one.
CREATE INDEX IF NOT EXISTS idx_audit_events_impersonated_by_time
    ON audit_events (impersonated_by, created_at DESC)
    WHERE impersonated_by IS NOT NULL;
