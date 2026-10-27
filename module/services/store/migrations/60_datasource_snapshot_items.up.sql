-- How many objects the last closing snapshot of an object-storage source named.
-- The host owns deletion authority: a sync whose listing comes back empty for a
-- source that has files must not close, because a closing snapshot with no
-- files tombstones every entry mirrored from that source, and a store or
-- service that wrongly answers "empty" is the case that has already happened.
-- This is the host's record of "has files", written only when a closing
-- snapshot is sent. NULL on a source synced before it existed: unknown, which
-- the host treats as possibly holding files.
ALTER TABLE public.datasource_sources
  ADD COLUMN last_snapshot_items integer CHECK (last_snapshot_items >= 0);
