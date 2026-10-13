-- Reverts migration 37: domain_events no longer records the transaction that wrote
-- a row. Dropping the column drops its default and the index on its expression
-- with it; the index is dropped first and by name so that the revert says what it
-- removes. Nothing else reads xact_id, so no other object changes. The ids are
-- not recoverable once dropped: a later migration 37 leaves every existing row
-- NULL, which reads as transaction 0.
DROP INDEX IF EXISTS public.idx_domain_events_xact_seq;
ALTER TABLE public.domain_events DROP COLUMN IF EXISTS xact_id;
