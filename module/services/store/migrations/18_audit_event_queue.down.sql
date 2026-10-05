-- Removing the queue removes every event still in it. Under the default
-- (postgres) and the tee (both) nothing writes it; under a swap value, roll back
-- only once the relay has drained it.
DROP TABLE IF EXISTS public.audit_event_queue;
