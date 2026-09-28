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
