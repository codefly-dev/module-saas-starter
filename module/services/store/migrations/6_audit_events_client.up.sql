-- An audited call made through a registered client has been indistinguishable
-- from the same person's call from the host's own web session: the access token
-- carries the client as `azp` and the gateway forwards it, but the row had
-- nowhere to keep it. The column separates "who did this" from "what did they
-- do it through".
--
-- NULL is the ordinary value — a first-party web session names no client — and
-- so is the right reading for every row written before this migration, which is
-- why the column is nullable with no backfill. The parent is RANGE-partitioned;
-- ADD COLUMN propagates to every existing and future partition.
ALTER TABLE public.audit_events ADD COLUMN client_id text;

-- The trail is filtered by client as well as by actor ("what did this client
-- do"), on a relation that only ever grows. Partial, like
-- idx_audit_events_impersonated_by_time: the overwhelming majority of rows name
-- no client, and a first-party session is never what this filter asks for.
--
-- Written WITHOUT `ON ONLY`, unlike the shape the baseline carries. The
-- baseline is a pg_dump rendering, which splits a partitioned index into an
-- `ON ONLY` parent plus one ATTACHed child per partition; issuing `ON ONLY`
-- here would instead leave an invalid parent index with no children and no
-- partition ever using it. Plain CREATE INDEX on the parent recurses and
-- attaches. It holds ACCESS EXCLUSIVE on each partition while it builds —
-- CONCURRENTLY is not available on a partitioned table — so audit writes block
-- for the duration; the predicate keeps that to the rows that name a client.
CREATE INDEX idx_audit_events_client_id_time ON public.audit_events
  USING btree (client_id, created_at DESC) WHERE client_id IS NOT NULL;
