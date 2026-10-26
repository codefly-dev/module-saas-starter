-- Refuse a rollback while it would strand signed endpoint delivery history.
-- The inbox is append-only: downgrading must not silently delete that evidence.
ALTER TABLE public.solution_delivery_documents
    DROP CONSTRAINT solution_delivery_documents_kind_check,
    ADD CONSTRAINT solution_delivery_documents_kind_check
        CHECK (kind IN ('presence', 'authority'));
