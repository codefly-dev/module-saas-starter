-- Ordered delivery must break ties by enqueue order, not by the random
-- primary-key UUID (issue #493 review, finding F1).
--
-- The event relay enqueues an entire batch of inbox jobs inside ONE transaction
-- (see PostgresEventTransport.relayBatch). Postgres CURRENT_TIMESTAMP / NOW() is
-- transaction-start time, so every job written in that batch shares one identical
-- created_at. The claim fence and the claim ORDER BY (postgres_jobs.go,
-- claimJobsSQL) broke that created_at tie on job_messages.id, which is
-- gen_random_uuid() -- random. Two same-partition ordered events drained in a
-- single relay batch were therefore handed to the worker in random order,
-- silently defeating the ordered-delivery guarantee on the shipped worker path.
--
-- enqueue_seq is a monotonic, insert-ordered discriminator: a bigint identity
-- assigned in physical insert order. Fencing and ordering on
-- (created_at, enqueue_seq) instead of (created_at, id) makes the tiebreak follow
-- enqueue order -- deterministic and correct -- while remaining byte-identical to
-- the old behavior whenever created_at already differs (every caller that enqueues
-- in its own transaction).
--
-- GENERATED ALWAYS is safe: no INSERT path supplies enqueue_seq or id.
-- enqueue_job_message (migrations 72, 75) and replay_job_message (72) both list
-- their INSERT columns explicitly and neither names enqueue_seq or id; the
-- direct-insert tests likewise omit them. The enforce_job_message_state
-- immutability trigger never lists enqueue_seq in an UPDATE SET, so it is never
-- rewritten after insert.

ALTER TABLE public.job_messages
    ADD COLUMN enqueue_seq BIGINT GENERATED ALWAYS AS IDENTITY;

-- Rebuild the ready and ordering indexes to end in enqueue_seq instead of id, so
-- the index order matches the new claim ORDER BY and fence and the planner can
-- still serve the ordered scan straight from the index.
DROP INDEX public.idx_job_messages_ready;
CREATE INDEX idx_job_messages_ready
    ON public.job_messages(queue, priority DESC, available_at, created_at, enqueue_seq)
    WHERE state IN ('pending', 'retrying');

DROP INDEX public.idx_job_messages_ordering;
CREATE INDEX idx_job_messages_ordering
    ON public.job_messages(queue, ordering_key, created_at, enqueue_seq)
    WHERE ordering_key IS NOT NULL AND state IN ('pending', 'processing', 'retrying');
