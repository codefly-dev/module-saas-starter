ALTER TABLE public.audit_event_types DROP CONSTRAINT audit_event_types_retention_class;
ALTER TABLE public.audit_event_types DROP COLUMN retention_class;
