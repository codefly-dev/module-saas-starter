-- The private custody surface is gone from the host: durable
-- execution belongs to the orchestration module, and the host carries no
-- knowledge of it. Dropping the table also drops its policies and index.
DROP TABLE public.execution_custody;
