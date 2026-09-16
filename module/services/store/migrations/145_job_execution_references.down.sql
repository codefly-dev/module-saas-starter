DROP INDEX IF EXISTS public.job_messages_datasource_delivery_correlation_idx;

ALTER TABLE public.job_messages
 DROP CONSTRAINT IF EXISTS job_messages_execution_check,
 DROP COLUMN IF EXISTS execution_id,
 DROP COLUMN IF EXISTS execution_kind,
 DROP COLUMN IF EXISTS execution_owner;
