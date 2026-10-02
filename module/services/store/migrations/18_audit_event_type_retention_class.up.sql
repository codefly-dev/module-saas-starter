-- An audit event type's retention class (ADR 0009): whether a warehouse store
-- of record keeps the type's full details for the compliance window
-- ('security') or for the shorter content window ('content'). The code-owned
-- catalog declares one per type and the startup projection writes it here, as
-- it writes visibility; a solution- or module-declared type states its own, or
-- gets 'content' by saying nothing.
--
-- 'content' is the default and the backfill: a declared row written before this
-- migration was declared under no class at all, which is what a declaration
-- that says nothing means; every code-owned row is rewritten from the catalog
-- at the next startup.
ALTER TABLE public.audit_event_types
  ADD COLUMN retention_class text NOT NULL DEFAULT 'content';

-- The relay routes a type's details by this column, so a value outside the
-- vocabulary is refused at the table rather than read as either class.
ALTER TABLE public.audit_event_types
  ADD CONSTRAINT audit_event_types_retention_class CHECK (retention_class IN ('security', 'content'));
