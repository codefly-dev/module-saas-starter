-- One signed module manifest per binding replaces the presence, authority and
-- endpoint documents (codefly/module-manifest/v1). Core reads no document of
-- the replaced family, so their inbox rows can never be verified again; they
-- are removed and the inbox holds manifests only, keyed by binding.
DELETE FROM public.solution_delivery_documents WHERE kind <> 'manifest';
ALTER TABLE public.solution_delivery_documents
    DROP CONSTRAINT solution_delivery_documents_kind_check,
    ADD CONSTRAINT solution_delivery_documents_kind_check CHECK (kind = 'manifest');

-- Desired and live applied state were presence documents: the next delivered
-- manifest of each binding re-establishes them, keeping its installation
-- target. A recorded withdrawal stays as it is — it is terminal, and the host
-- refuses every later manifest for that binding.
UPDATE public.solution_host_bindings
   SET desired_generation = NULL, desired_digest = NULL, desired_document = NULL, desired_seen_at = NULL,
       pending_reason = NULL, pending_since = NULL;
UPDATE public.solution_host_bindings
   SET applied_generation = NULL, applied_digest = NULL, applied_document = NULL, applied_routes = '{}',
       applied_solution_id = NULL, applied_release = NULL, applied_domain = NULL, applied_at = NULL
 WHERE NOT applied_removed;

-- A manifest names a host coordinate and no component.
ALTER TABLE public.solution_host_bindings DROP COLUMN host_component;
