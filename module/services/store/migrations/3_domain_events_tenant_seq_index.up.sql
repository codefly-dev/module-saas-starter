-- A tenant reading its own journal forward keysets on seq under the org RLS
-- floor: `WHERE tenant_id = $1 AND seq > $2 ORDER BY seq`. No existing index
-- serves that shape — the primary key is on id, idx_domain_events_replay leads
-- with type, and idx_domain_events_unpublished is partial on rows the relay has
-- not published yet, which is the complement of what a reader sees. Without this
-- the read degrades to a scan of the whole relation, once per poll per connected
-- reader, and the cost grows with retained history rather than with the page.
--
-- Partial on tenant_id IS NOT NULL to match the reader's predicate and to leave
-- platform-scope events (which carry no tenant and are never on a tenant stream)
-- out of the index entirely.
CREATE INDEX IF NOT EXISTS idx_domain_events_tenant_seq
    ON public.domain_events USING btree (tenant_id, seq)
    WHERE (tenant_id IS NOT NULL);
