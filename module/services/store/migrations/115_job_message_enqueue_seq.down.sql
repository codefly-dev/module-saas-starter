-- Restore the (…, id) tiebreak indexes and drop the enqueue_seq discriminator.
DROP INDEX public.idx_job_messages_ready;
CREATE INDEX idx_job_messages_ready
    ON public.job_messages(queue, priority DESC, available_at, created_at, id)
    WHERE state IN ('pending', 'retrying');

DROP INDEX public.idx_job_messages_ordering;
CREATE INDEX idx_job_messages_ordering
    ON public.job_messages(queue, ordering_key, created_at, id)
    WHERE ordering_key IS NOT NULL AND state IN ('pending', 'processing', 'retrying');

ALTER TABLE public.job_messages
    DROP COLUMN enqueue_seq;
