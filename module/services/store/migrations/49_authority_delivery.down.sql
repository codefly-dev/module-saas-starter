ALTER TABLE public.solution_delivery_documents
 DROP CONSTRAINT authority_delivery_pair,
 DROP COLUMN authority_document,
 DROP COLUMN authority_envelope;
