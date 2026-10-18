-- Refuse a rollback while it would strand signed manifest history: the inbox
-- is append-only, and the replaced document family cannot hold a manifest.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM public.solution_delivery_documents WHERE kind = 'manifest') THEN
        RAISE EXCEPTION 'solution_delivery_documents holds module manifests; migration 41 cannot be reverted';
    END IF;
END $$;
ALTER TABLE public.solution_delivery_documents
    DROP CONSTRAINT solution_delivery_documents_kind_check,
    ADD CONSTRAINT solution_delivery_documents_kind_check
        CHECK (kind IN ('presence', 'authority', 'endpoints'));
ALTER TABLE public.solution_host_bindings ADD COLUMN host_component text;
UPDATE public.solution_host_bindings SET host_component = host_coordinate;
ALTER TABLE public.solution_host_bindings
    ALTER COLUMN host_component SET NOT NULL,
    ADD CONSTRAINT solution_host_bindings_host_component_check CHECK ((host_component <> ''::text));
ALTER TABLE public.solution_host_bindings DROP COLUMN first_domain;
