DROP INDEX public.idx_audit_events_client_id_time;
ALTER TABLE public.audit_events DROP COLUMN client_id;
