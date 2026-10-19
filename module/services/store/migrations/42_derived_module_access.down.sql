-- Reverting drops every derived grant with the column that names its cause: a
-- derived grant whose provenance is gone is indistinguishable from a grant an
-- administrator wrote, and leaving one behind would be exactly the "grant that
-- outlives its cause" this change exists to prevent.
DELETE FROM public.scope_grants WHERE derived_from_grant_id IS NOT NULL;
ALTER TABLE public.scope_grants DROP COLUMN derived_from_grant_id;

-- The derived roles go with them. They are this host's own rows, invisible in
-- every picker, and nothing but a derived grant ever referenced one.
DELETE FROM public.roles WHERE derived_from IS NOT NULL;
ALTER TABLE public.roles DROP COLUMN derived_from;
