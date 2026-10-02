-- An audit event type's visibility — how far an event of that type may travel —
-- had no column, because until solutions could declare their own types the
-- answer was compiled in: module-compose writes every code-owned type into the
-- event catalog with a `visibility`, and the emitter and the relay both read it
-- from there. A solution-declared type is registered here at runtime, where
-- compose never sees it, so it had no visibility at all and no declared type
-- could ever be delivered to a tenant's outbound webhook endpoint — while
-- subscribing to one still succeeded.
--
-- 'tenant' is the safe reading and therefore the default: an event stays inside
-- the platform unless its producer declared otherwise and the operator granted
-- its namespace external delivery. Backfilling every existing row to 'tenant'
-- is correct for both kinds. A declared row written before this migration was
-- declared under no visibility at all, so it never left the platform and must
-- not start now; a code-owned row is answered by the compiled catalog, which
-- the startup projection then rewrites into this column so one table can be
-- read for the whole registry.
ALTER TABLE public.audit_event_types
  ADD COLUMN visibility text NOT NULL DEFAULT 'tenant';

-- The gate reads this column, so a value outside the vocabulary is a row no
-- code path can act on: refuse it at the table rather than let each reader
-- invent a default. 'internal' has no reading here — an audit record is always
-- readable by its own tenant — so the catalog's third value is deliberately not
-- admitted.
ALTER TABLE public.audit_event_types
  ADD CONSTRAINT audit_event_types_visibility CHECK (visibility IN ('tenant', 'external'));

-- The relay is what decides whether an event may leave the platform, and it runs
-- as app_job_worker: that is the role holding SELECT, UPDATE on
-- public.domain_events, which is how it reads the journal and marks a row
-- published. Deciding deliverability for a type module-compose never saw means
-- reading this registry on that same transaction, so the role needs to see it.
--
-- Read-only, and strictly less than the role already holds: this table carries
-- type names, owners and payload schemas for a whole deployment and no tenant
-- rows at all, while domain_events — which app_job_worker already selects —
-- carries the event bodies themselves.
--
-- Without this grant the new path fails in a way that hides itself. The lookup
-- short-circuits on the compiled catalog, so every code-owned type keeps being
-- delivered and only a solution- or module-declared type reaches the table; the
-- read is refused, the relay treats a failed read as an error rather than a
-- "no" (it must — refusing on it would silently drop a delivery that is owed),
-- and the whole relay pass aborts. postgres_declared_audit_relay_test.go fails
-- without this line.
GRANT SELECT ON TABLE public.audit_event_types TO app_job_worker;
