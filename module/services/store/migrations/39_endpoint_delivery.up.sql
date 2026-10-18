-- Endpoint values reuse the signed delivery inbox and its append-only policy.
-- Presence remains address-free; the host passes these bytes to Codefly.
ALTER TABLE public.solution_delivery_documents
    DROP CONSTRAINT solution_delivery_documents_kind_check,
    ADD CONSTRAINT solution_delivery_documents_kind_check
        CHECK (kind IN ('presence', 'authority', 'endpoints'));
