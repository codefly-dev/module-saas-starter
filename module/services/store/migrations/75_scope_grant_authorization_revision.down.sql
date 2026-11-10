-- Reverts migration 75. Removing a scope grant no longer moves the
-- authorization revision; the revisions it already advanced are kept (a
-- revision only ever advances).

DROP TRIGGER scope_grants_bump_authorization_revision ON public.scope_grants;
DROP FUNCTION public.authorization_revision_scope_grant_mutation();
