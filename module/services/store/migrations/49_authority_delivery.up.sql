-- The signed pair is one immutable inbox row, never a separately mutable policy.
ALTER TABLE public.solution_delivery_documents
 ADD COLUMN authority_document bytea,
 ADD COLUMN authority_envelope bytea,
 ADD CONSTRAINT authority_delivery_pair CHECK ((authority_document IS NULL) = (authority_envelope IS NULL));
