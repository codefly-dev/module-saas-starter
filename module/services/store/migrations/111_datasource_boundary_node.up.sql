-- Bind every datasource to a data boundary (issue #473). The boundary below the
-- tenant is a scope node (migration 98, RFC-0001), not a second container table:
-- a datasource writes into a `collection` node's subtree, and that node is a
-- grant recipient like any other, so "share this collection with team B" becomes
-- an ordinary GrantScope. The free-text target_collection had no such spelling —
-- it was neither resolvable nor grantable — so it is replaced, not kept alongside.
--
-- Backfill: mint one `collection` scope node per distinct target_collection in an
-- org and point every source at it. A collection node is registered as a root
-- (a single encoded-UUID ltree label) here; once installation identity (#474)
-- lands it becomes a child of the org's solution node. The node id is the path,
-- lowercased with '-' -> '_' so it is a valid scope_nodes label; the human name
-- survives verbatim in `label`.
--
-- datasource_sources stays a TENANT relation with the same RLS policy and grants
-- (DATABASE_AUTHORITY.md); this only rewrites a column, so no classification or
-- grant change is needed. The FK to scope_nodes bypasses RLS like every RI check,
-- so cross-org misbinding is refused at the business boundary, not here.

ALTER TABLE datasource_sources
    ADD COLUMN boundary_node_id UUID REFERENCES scope_nodes(id);

WITH minted AS (
    SELECT DISTINCT org_id, target_collection, gen_random_uuid() AS node_id
    FROM datasource_sources
),
inserted AS (
    INSERT INTO scope_nodes (id, org_id, scope_path, kind, label)
    SELECT node_id, org_id, replace(node_id::text, '-', '_')::ltree, 'collection', target_collection
    FROM minted
    RETURNING id, org_id, label
)
UPDATE datasource_sources ds
    SET boundary_node_id = inserted.id
    FROM inserted
    WHERE ds.org_id = inserted.org_id
      AND ds.target_collection = inserted.label;

ALTER TABLE datasource_sources
    ALTER COLUMN boundary_node_id SET NOT NULL,
    DROP COLUMN target_collection;

-- The FK is probed both on source insert and when a scope node is deleted; index
-- it so neither pays a sequential scan as sources-per-org grows.
CREATE INDEX IF NOT EXISTS idx_datasource_sources_boundary
    ON datasource_sources (boundary_node_id);
