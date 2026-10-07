-- Dropping the table would silently re-point every organization that had a
-- binding at the deployment's own key, while its credentials stay sealed under
-- the key the binding named — unreadable, with nothing left to say why.
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM public.org_key_bindings) THEN
  RAISE EXCEPTION 'cannot remove organization key bindings while any organization is bound to its own key';
 END IF;
END $$;
DROP TABLE public.org_key_bindings;
DROP FUNCTION public.org_key_binding_revocation_is_terminal();
