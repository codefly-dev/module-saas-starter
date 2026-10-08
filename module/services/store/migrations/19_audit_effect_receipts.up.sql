-- Preserve historical guards without inventing their missing request intent.
-- New effect reservations bind the request and generated event ID atomically
-- with the audit write; NULL pairs remain explicitly unverifiable.
ALTER TABLE public.audit_event_idempotency
 ADD COLUMN request_fingerprint text,
 ADD COLUMN audit_event_id uuid,
 ADD CONSTRAINT audit_effect_binding_complete CHECK (
   (request_fingerprint IS NULL) = (audit_event_id IS NULL)
 );
