-- Reverts migration 22. The history copy would then have to retire partitions
-- through audit_events_drop_partitions_before again.
DROP FUNCTION public.audit_events_drop_verified_partitions(timestamp with time zone, text[], bigint[]);
