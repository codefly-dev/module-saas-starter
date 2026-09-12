-- The function is owned by app_control_plane and DROP requires ownership. A
-- managed migration principal is a non-superuser CREATEROLE role that does not
-- inherit it, so assume the role for the drop -- and hand the transaction back
-- before the ledger write, which no runtime role may perform.
SET LOCAL ROLE app_control_plane;
DROP FUNCTION IF EXISTS public.identity_administered_organizations(UUID);
RESET ROLE;
