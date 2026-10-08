ALTER TABLE public.audit_event_idempotency
 DROP CONSTRAINT audit_effect_binding_complete,
 DROP COLUMN audit_event_id,
 DROP COLUMN request_fingerprint;
