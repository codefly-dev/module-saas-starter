-- Reverts migration 13's helper. Partitions retain their row-level security;
-- a later change to the parent's policies again reaches only the months accounts
-- ensures.
DROP FUNCTION public.audit_events_secure_all_partitions();
