ALTER TABLE public.job_messages
 ADD COLUMN execution_owner TEXT,
 ADD COLUMN execution_kind TEXT,
 ADD COLUMN execution_id TEXT,
 ADD CONSTRAINT job_messages_execution_check CHECK (
  (execution_owner IS NULL AND execution_kind IS NULL AND execution_id IS NULL)
  OR (
   state = 'succeeded'
   AND execution_owner ~ '^[a-z][a-z0-9-]{0,62}$'
   AND execution_kind ~ '^[a-z][a-z0-9_.-]{0,63}$'
   AND length(execution_id) BETWEEN 1 AND 255
  )
 );

CREATE INDEX job_messages_datasource_delivery_correlation_idx
 ON public.job_messages ((attributes ->> 'datasource.delivery_id'))
 WHERE attributes ? 'datasource.delivery_id';
