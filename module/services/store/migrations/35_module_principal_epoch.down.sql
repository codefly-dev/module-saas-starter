-- Reverts migration 35. Installing a module, or changing its installation's
-- revision, no longer advances its principal's authorization revision. The rows
-- migration 35 and its trigger wrote are kept: a revision only ever advances, and
-- deleting one would hand every capability sealed to it an epoch of 0, which
-- names none.

DROP TRIGGER installation_principal_epoch ON public.installations;
DROP FUNCTION public.installation_principal_epoch();
