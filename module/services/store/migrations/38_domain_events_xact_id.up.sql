-- domain_events records the transaction that wrote each row, so a reader can tell
-- which rows can no longer change place.
--
-- WHY. seq is the order rows were inserted, not the order their transactions
-- commit: a transaction that inserted seq 10 and is still open does not stop one
-- that inserted seq 11 from committing first, so a reader that has seen seq 11 may
-- still be owed seq 10. A cursor on seq therefore skips a row whenever it advances
-- past an open writer (EVENTS.md describes the re-read window a consumer needs for
-- that). The journal read for the platform's own reader (ReadJournal, ADR 0009)
-- needs a cursor that cannot skip, and that is exact when it is gated on the
-- writing transaction: every transaction that can still commit took its id at or
-- after pg_snapshot_xmin(pg_current_snapshot()), so a row whose xact_id is below
-- that bound belongs to a transaction that has finished, and a cursor that only
-- serves such rows, in (xact_id, seq) order, never passes a row that can still
-- appear below it.
--
-- THE COLUMN. xact_id is the writing transaction's top-level id, set at insert by
-- a column default, pg_current_xact_id(). A default, not a change to
-- publish_domain_event: the only INSERT into this table is that function's, the
-- default fires there with the function's body, guard, advisory lock and grants
-- exactly as migration 37 left them, and nothing is replaced. pg_current_xact_id()
-- returns the top-level id even inside a subtransaction, which is the one
-- pg_snapshot_xmin compares against. The default is attached after the column is
-- added, not in the same statement: a volatile default on ADD COLUMN rewrites the
-- whole table under an exclusive lock, and the plain ADD COLUMN and SET DEFAULT
-- are catalog-only.
--
-- EXISTING ROWS ARE LEFT NULL, AND NULL IS READ AS TRANSACTION 0. A backfill would
-- rewrite every row of a table that has no enforced retention, only to invent a
-- value: the writing transactions are long gone and their ids are not recorded.
-- None is needed. Adding the column takes an ACCESS EXCLUSIVE lock, which waits
-- for every transaction that has written to the table to finish, so each row that
-- exists when the column appears is committed; and every row written afterwards
-- takes a real id. The NULL rows are therefore one finished block that sorts
-- before every row with an id, ordered among themselves by seq, which is how they
-- have always been ordered. A reader keys on COALESCE(xact_id, '0'), and the index
-- is on that same expression, so the legacy block and the new rows read as one
-- ordered sequence with no special case. Position 0 is not a transaction id, so
-- no row ever collides with it.
--
-- THE INDEX serves the read's shape: WHERE key > cursor AND key-below-the-frontier
-- ORDER BY key LIMIT n, where key is (COALESCE(xact_id, '0'), seq). It is built
-- without CONCURRENTLY, as migration 3's index on this table was: the runner
-- applies a migration in a transaction, where CONCURRENTLY is not allowed, and
-- the build blocks writers to the table for its duration.
--
-- STALLS. The frontier holds behind a write transaction that stays open (a stuck
-- session, a long migration), and behind rows whose ids are ahead of the cluster
-- after a logical restore into a new cluster. Both stall the reader without
-- losing anything.

ALTER TABLE public.domain_events ADD COLUMN xact_id xid8;
ALTER TABLE public.domain_events ALTER COLUMN xact_id SET DEFAULT pg_current_xact_id();

COMMENT ON COLUMN public.domain_events.xact_id IS
    'The top-level transaction id that wrote the row (pg_current_xact_id() at insert). NULL on a row that existed when this column was added; read as 0, before every real id. A row below pg_snapshot_xmin(pg_current_snapshot()) belongs to a finished transaction.';

CREATE INDEX idx_domain_events_xact_seq
    ON public.domain_events USING btree ((COALESCE(xact_id, '0'::xid8)), seq);
