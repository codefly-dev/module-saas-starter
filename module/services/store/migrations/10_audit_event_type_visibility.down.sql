-- Revoked first: the grant exists to serve the column, so leaving it behind
-- would keep an authority the schema no longer justifies.
REVOKE SELECT ON TABLE public.audit_event_types FROM app_job_worker;
ALTER TABLE public.audit_event_types DROP CONSTRAINT audit_event_types_visibility;
ALTER TABLE public.audit_event_types DROP COLUMN visibility;
